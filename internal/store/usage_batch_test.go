package store

import (
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestReportLargeCumulativeReplay(t *testing.T) {
	s, now := fixture(t)
	n := newNode(t, s, "large-replay")
	d := newDevice(t, s, "replay-device")
	desired, err := s.Desired(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	const minutes = 3998
	r := model.AgentReport{Revision: desired.Revision, CoreHealthy: true}
	for i := 0; i < minutes; i++ {
		r.Minutes = append(r.Minutes, model.UsageBucket{DeviceID: d.ID, Start: now.Add(-time.Duration(i)*time.Minute).Unix() / 60 * 60, Upload: 10, Download: 20})
	}
	// Duplicate keys and independently decreasing counters must preserve maxima,
	// including when duplicate entries would otherwise cross a SQL batch boundary.
	first := r.Minutes[0]
	first.Upload, first.Download = 11, 5
	r.Minutes = append(r.Minutes, first)
	first.Upload, first.Download = 9, 23
	r.Minutes = append(r.Minutes, first)
	r.Daily = []model.UsageBucket{
		{DeviceID: d.ID, Start: DayStart(*now), Upload: 40000, Download: 80000},
		{DeviceID: d.ID, Start: DayStart(*now), Upload: 39000, Download: 81000},
	}
	started := time.Now()
	if err := s.Report(n.ID, r); err != nil {
		t.Fatal(err)
	}
	initial := time.Since(started)
	started = time.Now()
	const replays = 3
	for i := 0; i < replays; i++ {
		if err := s.Report(n.ID, r); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("postgres=%t report_rows=%d initial=%s replay_mean=%s", s.Postgres(), len(r.Minutes), initial, time.Since(started)/replays)
	var count int
	var up, down int64
	if err := s.DB.QueryRow("SELECT COUNT(*),SUM(upload),SUM(download) FROM minute WHERE node_id=?", n.ID).Scan(&count, &up, &down); err != nil || count != minutes || up != minutes*10+1 || down != minutes*20+3 {
		t.Fatalf("minute replay: rows=%d upload=%d download=%d err=%v", count, up, down, err)
	}
	if err := s.DB.QueryRow("SELECT SUM(upload),SUM(download) FROM hourly WHERE node_id=?", n.ID).Scan(&up, &down); err != nil || up != minutes*10+1 || down != minutes*20+3 {
		t.Fatalf("hourly replay: upload=%d download=%d err=%v", up, down, err)
	}
	if err := s.DB.QueryRow("SELECT upload,download FROM lifetime WHERE node_id=? AND device_id=?", n.ID, d.ID).Scan(&up, &down); err != nil || up != 40000 || down != 81000 {
		t.Fatalf("lifetime replay: upload=%d download=%d err=%v", up, down, err)
	}
}
