package tui

import "testing"

func BenchmarkBuildDetail512K(b *testing.B) {
	m := bigModel(1, 512)
	rec := cleanRecord(m.exs[0].Snapshot())
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = buildDetail(rec, tabResponse, false, true, m.detailW()-1)
	}
}
