package store

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"sort"
	"strings"

	"github.com/KeepS13/HQIP/internal/model"
)

// Keep each statement below SQLite's conservative 999-parameter limit as well
// as bounding PostgreSQL parse/plan work. Both backends use one transaction.
const usageSQLBatchSize = 128

type usageKey struct {
	device string
	start  int64
}

// Collapse duplicate cumulative counters before paging. Applying only positive
// differences inside the serializable transaction keeps replay and concurrent
// reporters from adding the same bytes to lifetime/hourly totals twice.
func recordUsageBatch(ctx context.Context, tx *sql.Tx, table, node string, input []model.UsageBucket, valid map[string]bool, cutoff int64) error {
	if table != "daily" && table != "minute" && table != "native_hourly" {
		return errors.New("invalid usage table")
	}
	buckets := make(map[usageKey]model.UsageBucket, len(input))
	for _, b := range input {
		if !valid[b.DeviceID] || b.Start < cutoff || table == "native_hourly" && strings.HasPrefix(b.DeviceID, "__probe_") {
			continue
		}
		key := usageKey{b.DeviceID, b.Start}
		old := buckets[key]
		b.Upload, b.Download = max(old.Upload, b.Upload), max(old.Download, b.Download)
		b.Uncertain = old.Uncertain || b.Uncertain
		buckets[key] = b
	}
	keys := sortedUsageKeys(buckets)
	totals := make(map[usageKey]model.UsageBucket)
	for offset := 0; offset < len(keys); offset += usageSQLBatchSize {
		page := keys[offset:min(offset+usageSQLBatchSize, len(keys))]
		args := []any{node}
		for _, key := range page {
			args = append(args, key.device, key.start)
		}
		columns := "device_id,start,upload,download"
		if table == "native_hourly" {
			columns += ",uncertain"
		}
		rows, err := tx.QueryContext(ctx, "SELECT "+columns+" FROM "+table+" WHERE node_id=? AND (device_id,start) IN ("+usagePlaceholders(len(page), "(?,?)")+")", args...)
		if err != nil {
			return err
		}
		previous := make(map[usageKey]model.UsageBucket, len(page))
		for rows.Next() {
			var old model.UsageBucket
			values := []any{&old.DeviceID, &old.Start, &old.Upload, &old.Download}
			if table == "native_hourly" {
				values = append(values, &old.Uncertain)
			}
			if err := rows.Scan(values...); err != nil {
				rows.Close()
				return err
			}
			previous[usageKey{old.DeviceID, old.Start}] = old
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		var changed []model.UsageBucket
		for _, key := range page {
			b := buckets[key]
			old, exists := previous[key]
			b.Upload, b.Download = max(b.Upload, old.Upload), max(b.Download, old.Download)
			b.Uncertain = b.Uncertain || old.Uncertain
			if exists && b.Upload == old.Upload && b.Download == old.Download && (table != "native_hourly" || b.Uncertain == old.Uncertain) {
				continue
			}
			changed = append(changed, b)
			if table == "native_hourly" || b.Upload == old.Upload && b.Download == old.Download {
				continue
			}
			totalKey := usageKey{device: key.device}
			if table == "minute" {
				totalKey.start = key.start / 3600 * 3600
			}
			total := totals[totalKey]
			up, down := b.Upload-old.Upload, b.Download-old.Download
			if up > math.MaxInt64-total.Upload || down > math.MaxInt64-total.Download {
				return errors.New("流量汇总超出范围")
			}
			total.DeviceID, total.Start = totalKey.device, totalKey.start
			total.Upload += up
			total.Download += down
			totals[totalKey] = total
		}
		if err := writeUsageBatch(ctx, tx, table, node, changed, false); err != nil {
			return err
		}
	}
	totalTable := "lifetime"
	if table == "minute" {
		totalTable = "hourly"
	}
	keys = sortedUsageKeys(totals)
	for offset := 0; offset < len(keys); offset += usageSQLBatchSize {
		var page []model.UsageBucket
		for _, key := range keys[offset:min(offset+usageSQLBatchSize, len(keys))] {
			page = append(page, totals[key])
		}
		if err := writeUsageBatch(ctx, tx, totalTable, node, page, true); err != nil {
			return err
		}
	}
	return nil
}

func sortedUsageKeys(buckets map[usageKey]model.UsageBucket) []usageKey {
	keys := make([]usageKey, 0, len(buckets))
	for key := range buckets {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].device != keys[j].device {
			return keys[i].device < keys[j].device
		}
		return keys[i].start < keys[j].start
	})
	return keys
}

func usagePlaceholders(count int, row string) string {
	return strings.TrimSuffix(strings.Repeat(row+",", count), ",")
}

func writeUsageBatch(ctx context.Context, tx *sql.Tx, table, node string, buckets []model.UsageBucket, additive bool) error {
	if len(buckets) == 0 {
		return nil
	}
	columns, conflict, row := "node_id,device_id,start,upload,download", "node_id,device_id,start", "(?,?,?,?,?)"
	if table == "lifetime" {
		columns, conflict, row = "node_id,device_id,upload,download", "node_id,device_id", "(?,?,?,?)"
	} else if table == "native_hourly" {
		columns, row = columns+",uncertain", "(?,?,?,?,?,?)"
	}
	var args []any
	for _, b := range buckets {
		args = append(args, node, b.DeviceID)
		if table != "lifetime" {
			args = append(args, b.Start)
		}
		args = append(args, b.Upload, b.Download)
		if table == "native_hourly" {
			args = append(args, b.Uncertain)
		}
	}
	update := "upload=MAX(" + table + ".upload,excluded.upload),download=MAX(" + table + ".download,excluded.download)"
	if additive {
		update = "upload=" + table + ".upload+excluded.upload,download=" + table + ".download+excluded.download"
	} else if table == "native_hourly" {
		update += ",uncertain=MAX(native_hourly.uncertain,excluded.uncertain)"
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO "+table+"("+columns+") VALUES"+usagePlaceholders(len(buckets), row)+" ON CONFLICT("+conflict+") DO UPDATE SET "+update, args...)
	return err
}
