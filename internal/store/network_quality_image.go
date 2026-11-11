package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/KeepS13/HQIP/internal/qualityimage"
)

// The generated image belongs to exactly one report and is stored in that same
// row. Existing PostgreSQL application privileges and backups cover it; no DDL
// or filesystem cache is needed. This field is never exposed by status reads.
const qualityImageField = "_hqipImage"

func splitQualityImage(raw json.RawMessage) (json.RawMessage, string, bool, error) {
	if string(raw) == "null" {
		return raw, "", false, nil
	}
	var doc map[string]json.RawMessage
	if len(raw) > 4<<20 || json.Unmarshal(raw, &doc) != nil || doc == nil {
		return nil, "", false, errors.New("检测报告格式无效")
	}
	var image string
	available := true
	if value, ok := doc[qualityImageField]; ok {
		if json.Unmarshal(value, &image) != nil || len(image) > qualityimage.MaxSVG {
			return nil, "", false, errors.New("检测图片格式无效")
		}
		available = image != ""
		delete(doc, qualityImageField)
	}
	delete(doc, "HQIPReportANSI")
	clean, err := json.Marshal(doc)
	return clean, image, available, err
}

func qualityImageVersion(report json.RawMessage, at int64, ip string) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("hqip-quality-image-v1\n%d\n%s\n%s", at, ip, report)))
	return hex.EncodeToString(digest[:])
}

func attachQualityImage(report json.RawMessage, image []byte) (json.RawMessage, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(report, &doc); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(string(image))
	if err != nil {
		return nil, err
	}
	doc[qualityImageField] = encoded
	return json.Marshal(doc)
}

func prepareQualityImage(report json.RawMessage) (json.RawMessage, error) {
	var doc map[string]json.RawMessage
	if json.Unmarshal(report, &doc) != nil || doc == nil {
		return nil, errors.New("检测报告格式无效")
	}
	var ansi string
	if value, ok := doc["HQIPReportANSI"]; ok {
		if json.Unmarshal(value, &ansi) != nil || len(ansi) > qualityimage.MaxANSI {
			ansi = ""
		}
		delete(doc, "HQIPReportANSI")
	}
	// An external diagnostic may supply data, never its own executable markup.
	delete(doc, qualityImageField)
	clean, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	if len(clean) > 512<<10 {
		return nil, errors.New("检测报告超过大小限制")
	}
	image, err := qualityimage.Render(clean, ansi)
	if err != nil {
		// Optional presentation must not discard a successful factual report.
		image, err = qualityimage.Render(clean, "")
		if err != nil {
			image = nil
		}
	}
	return attachQualityImage(clean, image)
}

// NetworkQualityImage returns the saved image for the exact visible report.
// A legacy JSON-only report is rendered once and cached with a compare-and-swap,
// so a concurrent completed scan cannot receive an older report's image.
func (s *Store) NetworkQualityImage(id, version string) ([]byte, error) {
	var raw, ip string
	var at int64
	err := s.DB.QueryRow("SELECT report,report_at,report_ip FROM node_network_quality WHERE node_id=?", id).Scan(&raw, &at, &ip)
	if err != nil {
		return nil, notFound(err)
	}
	report, image, available, err := splitQualityImage(json.RawMessage(raw))
	if err != nil {
		return nil, err
	}
	if at <= 0 || !available || string(report) == "null" {
		return nil, ErrNotFound
	}
	if version != qualityImageVersion(report, at, ip) {
		return nil, ErrConflict
	}
	if image != "" {
		return []byte(image), nil
	}
	rendered, err := qualityimage.Render(report, "")
	if err != nil {
		return nil, err
	}
	stored, err := attachQualityImage(report, rendered)
	if err != nil {
		return nil, err
	}
	res, err := s.DB.Exec("UPDATE node_network_quality SET report=? WHERE node_id=? AND report=? AND report_at=? AND report_ip=?", string(stored), id, raw, at, ip)
	if err != nil {
		return nil, err
	}
	count, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if count == 0 {
		// Another reader may have cached the same image, or a new scan won.
		var current string
		err = s.DB.QueryRow("SELECT report FROM node_network_quality WHERE node_id=? AND report_at=? AND report_ip=?", id, at, ip).Scan(&current)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrConflict
		}
		if err != nil {
			return nil, err
		}
		latest, cached, _, err := splitQualityImage(json.RawMessage(current))
		if err != nil {
			return nil, err
		}
		if qualityImageVersion(latest, at, ip) != version || cached == "" {
			return nil, ErrConflict
		}
		return []byte(cached), nil
	}
	return rendered, nil
}
