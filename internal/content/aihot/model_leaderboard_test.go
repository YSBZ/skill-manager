package aihot

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"skillmanage/internal/content"
)

func TestParseModelLeaderboardPreservesSourceFieldsAndNotes(t *testing.T) {
	snapshot, err := parseModelLeaderboard("coding", []byte(modelLeaderboardTestHTML()))
	if err != nil {
		t.Fatalf("parse model leaderboard: %v", err)
	}
	if snapshot.Category != "coding" || snapshot.Title != "编程模型排行榜 · AIHOT" || snapshot.SourceURL != "https://aihot.news/leaderboard/category/coding" {
		t.Fatalf("source identity = category %q title %q URL %q", snapshot.Category, snapshot.Title, snapshot.SourceURL)
	}
	if snapshot.UpdatedLabel != "39 项评测·7 家机构·10/29 14:06 更新" {
		t.Fatalf("updated label=%q", snapshot.UpdatedLabel)
	}
	if snapshot.Currency != "CNY" || snapshot.PriceUnit != "人民币 / 百万 Token" || len(snapshot.Entries) != 2 {
		t.Fatalf("currency=%q price unit=%q entries=%d", snapshot.Currency, snapshot.PriceUnit, len(snapshot.Entries))
	}
	first := snapshot.Entries[0]
	if first.ID != "alpha" || first.Rank != 1 || first.Name != "Alpha 1" || first.Provider != "Vendor A" || first.ReleasedOn != "2026-10-28" {
		t.Fatalf("entry identity=%+v", first)
	}
	if first.CoveragePercent == nil || *first.CoveragePercent != 43 || first.BenchmarkCount == nil || *first.BenchmarkCount != 16 || first.Score != 74.3 {
		t.Fatalf("entry measurements=%+v", first)
	}
	if first.CachePrice != "待核验" || first.InputPrice != "¥26.67" || first.OutputPrice != "¥133.33" {
		t.Fatalf("entry prices=%q / %q / %q", first.CachePrice, first.InputPrice, first.OutputPrice)
	}
	if first.URL != "https://aihot.news/leaderboard/alpha" || first.OpenWeightsURL != "https://huggingface.co/vendor/alpha" {
		t.Fatalf("entry links=%q / %q", first.URL, first.OpenWeightsURL)
	}
	if snapshot.Entries[1].CoveragePercent != nil || snapshot.Entries[1].BenchmarkCount != nil {
		t.Fatalf("unavailable measurements must remain null: %+v", snapshot.Entries[1])
	}
	if len(snapshot.PriceNotes) != 2 || !strings.Contains(snapshot.PriceNotes[0], "限时") || !strings.Contains(snapshot.PriceNotes[1], "按每百万 Token 展示") || !strings.Contains(snapshot.PriceNotes[1], "缓存写入") {
		t.Fatalf("price notes=%q", snapshot.PriceNotes)
	}
}

func TestParseModelLeaderboardRejectsIncompleteOrUnsafeSnapshots(t *testing.T) {
	base := modelLeaderboardTestHTML()
	cases := []struct {
		name string
		html string
		cat  string
	}{
		{name: "unknown category", html: base, cat: "other"},
		{name: "caption row mismatch", html: strings.Replace(base, "当前展示 <!-- -->2<!-- --> 个模型", "当前展示 <!-- -->3<!-- --> 个模型", 1), cat: "coding"},
		{name: "missing semantic header", html: strings.Replace(base, "上线日期", "发行日", 1), cat: "coding"},
		{name: "duplicate model ID", html: strings.Replace(base, `data-slug="beta"`, `data-slug="alpha"`, 1), cat: "coding"},
		{name: "duplicate rank", html: strings.Replace(base, "<td>02</td>", "<td>01</td>", 1), cat: "coding"},
		{name: "untrusted detail URL", html: strings.Replace(base, "/leaderboard/alpha?from=coding", "https://evil.example/alpha", 1), cat: "coding"},
		{name: "non-finite score", html: strings.Replace(base, ">74.3</strong>", ">NaN</strong>", 1), cat: "coding"},
		{name: "truncated after complete table", html: modelLeaderboardHTMLAfterTable(), cat: "coding"},
		{name: "truncated before price basis after conditional notes", html: modelLeaderboardHTMLBeforePriceBasis(), cat: "coding"},
		{name: "missing price basis section", html: strings.Replace(base, `<section class="price-basis"><h2>关于价格</h2><p>API 价格来自厂商官网，按每百万 Token 展示并统一折算成人民币。 缓存价格指命中后的输入价格，缓存写入、存储及订阅费用另计。</p></section>`, "", 1), cat: "coding"},
		{name: "empty price basis paragraph", html: strings.Replace(base, `<p>API 价格来自厂商官网，按每百万 Token 展示并统一折算成人民币。 缓存价格指命中后的输入价格，缓存写入、存储及订阅费用另计。</p>`, `<p></p>`, 1), cat: "coding"},
		{name: "price basis without unit and currency", html: strings.Replace(base, `API 价格来自厂商官网，按每百万 Token 展示并统一折算成人民币。 缓存价格指命中后的输入价格，缓存写入、存储及订阅费用另计。`, `API 价格请查看厂商官网。`, 1), cat: "coding"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseModelLeaderboard(tc.cat, []byte(tc.html)); err == nil {
				t.Fatal("malformed model leaderboard accepted")
			}
		})
	}
}

func TestParseModelLeaderboardAllowsNoModelSpecificPriceConditionsWithBasis(t *testing.T) {
	page := strings.Replace(modelLeaderboardTestHTML(), `<section><p>模型条件价格：Alpha 1 在限时活动期间有特别报价。</p></section>`, "", 1)
	snapshot, err := parseModelLeaderboard("coding", []byte(page))
	if err != nil {
		t.Fatalf("complete source price basis without model-specific conditions: %v", err)
	}
	if len(snapshot.PriceNotes) != 1 || !strings.Contains(snapshot.PriceNotes[0], "按每百万 Token 展示") {
		t.Fatalf("price notes without conditional rows=%q", snapshot.PriceNotes)
	}
}

func TestFetchModelLeaderboardRequestsHTMLAndHonorsETag(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodGet || r.URL.Path != "/leaderboard/category/coding" || r.Header.Get("Accept") != "text/html" || r.Header.Get("If-None-Match") != `"known"` {
			t.Errorf("request method=%s path=%s accept=%q etag=%q", r.Method, r.URL.Path, r.Header.Get("Accept"), r.Header.Get("If-None-Match"))
		}
		w.Header().Set("ETag", `"known"`)
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.Header().Set("Last-Modified", "Thu, 29 Oct 2026 10:00:00 GMT")
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()

	result, err := NewClient(srv.URL, srv.Client()).FetchModelLeaderboard(context.Background(), "coding", `"known"`)
	if err != nil || !result.NotModified || result.ETag != `"known"` || result.CacheControl != "public, max-age=60" || calls != 1 {
		t.Fatalf("conditional fetch = %+v, %v, calls=%d", result, err, calls)
	}
	if _, err := NewClient(srv.URL, srv.Client()).FetchModelLeaderboard(context.Background(), "unknown", ""); err == nil {
		t.Fatal("unknown category accepted")
	}
}

func TestFetchModelLeaderboardParsesHTMLResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/leaderboard/category/coding" || r.Header.Get("Accept") != "text/html" {
			t.Errorf("request path=%s accept=%q", r.URL.Path, r.Header.Get("Accept"))
		}
		w.Header().Set("ETag", `"overall-v1"`)
		w.Header().Set("Cache-Control", "max-age=60")
		_, _ = io.WriteString(w, modelLeaderboardTestHTML())
	}))
	defer srv.Close()

	result, err := NewClient(srv.URL, srv.Client()).FetchModelLeaderboard(context.Background(), "coding", "")
	if err != nil || result.Snapshot.Category != "coding" || result.ETag != `"overall-v1"` || len(result.Snapshot.Entries) != 2 {
		t.Fatalf("HTML fetch = %+v, %v", result, err)
	}
}

func TestRefreshModelLeaderboardUsesHourlyCheckpoint304AndRetainsFailedSnapshot(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	mode := "success"
	calls := 0
	var conditionalETags []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/leaderboard/category/coding" || r.Header.Get("Accept") != "text/html" {
			t.Errorf("request path=%s accept=%q", r.URL.Path, r.Header.Get("Accept"))
		}
		conditionalETags = append(conditionalETags, r.Header.Get("If-None-Match"))
		switch mode {
		case "not-modified":
			w.Header().Set("ETag", `"model-v1"`)
			w.WriteHeader(http.StatusNotModified)
		case "truncated-after-table":
			w.Header().Set("ETag", `"model-v2"`)
			_, _ = io.WriteString(w, modelLeaderboardHTMLAfterTable())
		case "truncated-before-price-basis":
			w.Header().Set("ETag", `"model-v2"`)
			_, _ = io.WriteString(w, modelLeaderboardHTMLBeforePriceBasis())
		default:
			w.Header().Set("ETag", `"model-v1"`)
			w.Header().Set("Cache-Control", "public, max-age=60")
			_, _ = io.WriteString(w, modelLeaderboardTestHTML())
		}
	}))
	defer srv.Close()
	refresher := NewRefresher(NewClient(srv.URL, srv.Client()), store)
	now := time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
	if err := refresher.refreshModelLeaderboard(ctx, "coding", now); err != nil {
		t.Fatalf("initial model refresh: %v", err)
	}
	initial, err := store.LoadModelLeaderboard(ctx, "coding")
	if err != nil || initial.FetchedAt != content.ModelLeaderboardFetchedAt(now) || initial.Entries[0].CachePrice != "待核验" {
		t.Fatalf("initial model cache=%+v err=%v", initial, err)
	}
	checkpoint := modelLeaderboardCheckpoint(t, store, "coding")
	if !checkpoint.NextFetchAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("short source max-age weakened hourly refresh minimum: next=%s", checkpoint.NextFetchAt)
	}
	if err := refresher.refreshModelLeaderboard(ctx, "coding", now.Add(30*time.Minute)); err != nil || calls != 1 {
		t.Fatalf("refresh before hourly deadline: calls=%d err=%v", calls, err)
	}

	mode = "not-modified"
	secondFetchAt := now.Add(61 * time.Minute)
	if err := refresher.refreshModelLeaderboard(ctx, "coding", secondFetchAt); err != nil {
		t.Fatalf("304 model refresh: %v", err)
	}
	if calls != 2 || len(conditionalETags) != 2 || conditionalETags[1] != `"model-v1"` {
		t.Fatalf("conditional requests=%v calls=%d", conditionalETags, calls)
	}
	checkpoint = modelLeaderboardCheckpoint(t, store, "coding")
	if checkpoint.Status.State != "fresh" || !checkpoint.NextFetchAt.Equal(secondFetchAt.Add(time.Hour)) {
		t.Fatalf("304 freshness checkpoint=%+v", checkpoint)
	}

	mode = "truncated-after-table"
	failureAt := now.Add(122 * time.Minute)
	if err := refresher.refreshModelLeaderboard(ctx, "coding", failureAt); err != nil {
		t.Fatalf("malformed refresh should be represented as stale state: %v", err)
	}
	retained, err := store.LoadModelLeaderboard(ctx, "coding")
	if err != nil || retained.FetchedAt != initial.FetchedAt || retained.Entries[0].Name != initial.Entries[0].Name || strings.Join(retained.PriceNotes, "\n") != strings.Join(initial.PriceNotes, "\n") {
		t.Fatalf("malformed source replaced good cache: retained=%+v err=%v", retained, err)
	}
	checkpoint = modelLeaderboardCheckpoint(t, store, "coding")
	if checkpoint.Status.State != "stale" || checkpoint.FailureCount != 1 || !checkpoint.NextFetchAt.Equal(failureAt.Add(time.Hour)) {
		t.Fatalf("malformed source failure checkpoint=%+v", checkpoint)
	}

	mode = "truncated-before-price-basis"
	secondFailureAt := failureAt.Add(61 * time.Minute)
	if err := refresher.refreshModelLeaderboard(ctx, "coding", secondFailureAt); err != nil {
		t.Fatalf("truncation before price basis should retain cache: %v", err)
	}
	retained, err = store.LoadModelLeaderboard(ctx, "coding")
	if err != nil || retained.FetchedAt != initial.FetchedAt || strings.Join(retained.PriceNotes, "\n") != strings.Join(initial.PriceNotes, "\n") {
		t.Fatalf("mid-notes truncation replaced good cache: retained=%+v err=%v", retained, err)
	}
	checkpoint = modelLeaderboardCheckpoint(t, store, "coding")
	if checkpoint.Status.State != "stale" || checkpoint.FailureCount != 2 || !checkpoint.NextFetchAt.Equal(secondFailureAt.Add(time.Hour)) {
		t.Fatalf("mid-notes truncation checkpoint=%+v", checkpoint)
	}
	catalog := content.NewCatalog(store, func() time.Time { return secondFailureAt })
	stale, err := catalog.ModelLeaderboard(ctx, "coding")
	if err != nil || !stale.Meta.Stale || stale.Meta.LastError == "" || stale.Entries[0].Name != initial.Entries[0].Name {
		t.Fatalf("stale response=%+v err=%v", stale, err)
	}
}

func TestRefreshModelLeaderboardHonorsLongRetryAfter(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Retry-After", "2592000")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	refresher := NewRefresher(NewClient(srv.URL, srv.Client()), store)
	now := time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
	if err := refresher.refreshModelLeaderboard(ctx, "overall", now); err != nil {
		t.Fatalf("rate-limit state should be persisted: %v", err)
	}
	checkpoint := modelLeaderboardCheckpoint(t, store, "overall")
	if checkpoint.Status.State != "rate_limited" || !checkpoint.NextFetchAt.Equal(now.Add(30*24*time.Hour)) {
		t.Fatalf("long Retry-After checkpoint=%+v", checkpoint)
	}
	if err := refresher.refreshModelLeaderboard(ctx, "overall", now.Add(24*time.Hour)); err != nil || calls != 1 {
		t.Fatalf("retry happened before Retry-After: calls=%d err=%v", calls, err)
	}
}

func modelLeaderboardCheckpoint(t *testing.T, store interface {
	Checkpoints(context.Context) ([]content.SourceCheckpoint, error)
}, category string) content.SourceCheckpoint {
	t.Helper()
	checkpoints, err := store.Checkpoints(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	name := modelLeaderboardSourceNamePrefix + category
	for _, checkpoint := range checkpoints {
		if checkpoint.Name == name {
			return checkpoint
		}
	}
	t.Fatalf("missing checkpoint %q", name)
	return content.SourceCheckpoint{}
}

func modelLeaderboardTestHTML() string {
	return `<!doctype html><html><head><title>编程模型排行榜 · AIHOT</title></head><body>
<header><p class="num text-small">39 项评测·7 家机构·10/29 14:06 更新</p></header>
<table><caption>当前展示 <!-- -->2<!-- --> 个模型，可按列重排；名次来自原榜</caption>
<thead><tr><th>名次</th><th>模型</th><th>上线日期</th><th>评测覆盖</th><th>缓存价格人民币 / 百万 Token</th><th>输入价格</th><th>输出价格</th><th>AIHOT 评分</th></tr></thead><tbody>
<tr data-slug="alpha"><td>01</td><td><a href="/leaderboard/alpha?from=coding"><strong>Alpha 1</strong><small>Vendor A</small><small>Vendor A</small></a><a href="https://huggingface.co/vendor/alpha">开源权重</a></td><td><time datetime="2026-10-28">2026-10-28</time></td><td>16 项评测覆盖 43%</td><td>待核验</td><td>¥26.67</td><td>¥133.33</td><td><strong aria-label="Alpha 1 AIHOT 评分 74.3">74.3</strong><small>覆盖 43%</small></td></tr>
<tr data-slug="beta"><td>02</td><td><a href="/leaderboard/beta?from=coding"><strong>Beta 2</strong><small>Vendor B</small><small>Vendor B</small></a></td><td><time datetime="2026-09-22">2026-09-22</time></td><td>尚无公开评测覆盖</td><td>—</td><td>—</td><td>—</td><td><strong aria-label="Beta 2 AIHOT 评分 50.0">50.0</strong></td></tr>
</tbody></table>
<section><p>模型条件价格：Alpha 1 在限时活动期间有特别报价。</p></section>
<section class="price-basis"><h2>关于价格</h2><p>API 价格来自厂商官网，按每百万 Token 展示并统一折算成人民币。 缓存价格指命中后的输入价格，缓存写入、存储及订阅费用另计。</p></section>
</body></html>`
}

func modelLeaderboardHTMLAfterTable() string {
	page := modelLeaderboardTestHTML()
	end := strings.Index(page, "</table>") + len("</table>")
	if end < len("</table>") {
		return page
	}
	return page[:end]
}

func modelLeaderboardHTMLBeforePriceBasis() string {
	page := modelLeaderboardTestHTML()
	start := strings.Index(page, `<section class="price-basis">`)
	if start < 0 {
		return page
	}
	return page[:start]
}
