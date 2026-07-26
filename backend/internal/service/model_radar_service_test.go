package service

import "testing"

func TestParseModelRadarHTMLSupportsCurrentQualitySection(t *testing.T) {
	body := []byte(`
		<html><body>
			<section class="quota-radar"><h2>额度雷达 7月25日09:00更新</h2></section>
			<section class="fast-radar"><h2>Fast 雷达 7月25日09:00更新</h2></section>
			<article class="model-ratings">
				<h3>社区体感分</h3>
				<p>近 24 小时社区评分</p>
			</article>
		</body></html>`)

	snapshot, err := parseModelRadarHTML(body)
	if err != nil {
		t.Fatalf("parseModelRadarHTML() error = %v", err)
	}
	if snapshot.Quality.Title != "社区体感分" {
		t.Fatalf("quality title = %q, want %q", snapshot.Quality.Title, "社区体感分")
	}
	if len(snapshot.Quality.Cards) != 0 {
		t.Fatalf("quality cards = %d, want 0 for client-rendered source data", len(snapshot.Quality.Cards))
	}
}

func TestParseModelRadarHTMLAllowsMissingOptionalQualitySection(t *testing.T) {
	body := []byte(`
		<html><body>
			<section class="quota-radar"><h2>额度雷达</h2></section>
			<section class="fast-radar"><h2>Fast 雷达</h2></section>
		</body></html>`)

	snapshot, err := parseModelRadarHTML(body)
	if err != nil {
		t.Fatalf("parseModelRadarHTML() error = %v", err)
	}
	if snapshot.Quality.Title == "" {
		t.Fatal("quality title is empty")
	}
}
