package aihot

import "encoding/json"

type sourceWire struct {
	Name string `json:"name"`
}

type attributionWire struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type itemLinksWire struct {
	AIHOT    string `json:"aihot"`
	Original string `json:"original"`
}

type itemWire struct {
	ID            string           `json:"id"`
	Title         string           `json:"title"`
	OriginalTitle *string          `json:"originalTitle"`
	Summary       *string          `json:"summary"`
	Reason        *string          `json:"reason"`
	Category      *string          `json:"category"`
	Score         *float64         `json:"score"`
	Selected      bool             `json:"selected"`
	PublishedAt   *string          `json:"publishedAt"`
	DiscoveredAt  string           `json:"discoveredAt"`
	Source        sourceWire       `json:"source"`
	Links         itemLinksWire    `json:"links"`
	Attribution   *attributionWire `json:"attribution"`
}

type pageWire struct {
	Count      int     `json:"count"`
	HasMore    bool    `json:"hasMore"`
	NextCursor *string `json:"nextCursor"`
}

type itemsQueryWire struct {
	Mode     string  `json:"mode"`
	Category *string `json:"category"`
	Window   string  `json:"window"`
	Q        *string `json:"q"`
	By       string  `json:"by"`
	Ordering string  `json:"ordering"`
}

type itemsResponseWire struct {
	SchemaVersion int             `json:"schemaVersion"`
	Query         itemsQueryWire  `json:"query"`
	Items         []itemWire      `json:"items"`
	Page          *pageWire       `json:"page"`
	Notice        json.RawMessage `json:"notice"`
}

type issueLinksWire struct {
	AIHOT string `json:"aihot"`
}

type articleLinksWire struct {
	AIHOT    *string `json:"aihot"`
	Original string  `json:"original"`
}

type articleWire struct {
	Title       string           `json:"title"`
	Summary     string           `json:"summary"`
	Source      sourceWire       `json:"source"`
	Links       articleLinksWire `json:"links"`
	PublishedAt *string          `json:"publishedAt"`
	Attribution *attributionWire `json:"attribution"`
}

type dailyLeadWire struct {
	Title         string `json:"title"`
	LeadParagraph string `json:"leadParagraph"`
}

type dailySectionWire struct {
	Label string        `json:"label"`
	Items []articleWire `json:"items"`
}

type dailyFlashWire struct {
	Title       string           `json:"title"`
	Source      sourceWire       `json:"source"`
	Links       articleLinksWire `json:"links"`
	PublishedAt string           `json:"publishedAt"`
	Attribution *attributionWire `json:"attribution"`
}

type dailyWire struct {
	Date        string             `json:"date"`
	GeneratedAt string             `json:"generatedAt"`
	WindowStart string             `json:"windowStart"`
	WindowEnd   string             `json:"windowEnd"`
	Links       issueLinksWire     `json:"links"`
	Attribution *attributionWire   `json:"attribution"`
	Lead        *dailyLeadWire     `json:"lead"`
	Sections    []dailySectionWire `json:"sections"`
	Flashes     []dailyFlashWire   `json:"flashes"`
}

type dailyResponseWire struct {
	SchemaVersion int             `json:"schemaVersion"`
	Report        dailyWire       `json:"report"`
	Notice        json.RawMessage `json:"notice"`
}

type periodSectionWire struct {
	Label   string        `json:"label"`
	Summary *string       `json:"summary"`
	Items   []articleWire `json:"items"`
}

type periodWire struct {
	Week        string              `json:"week"`
	Month       string              `json:"month"`
	GeneratedAt string              `json:"generatedAt"`
	PeriodStart *string             `json:"periodStart"`
	PeriodEnd   *string             `json:"periodEnd"`
	WindowStart string              `json:"windowStart"`
	WindowEnd   string              `json:"windowEnd"`
	Links       issueLinksWire      `json:"links"`
	Attribution *attributionWire    `json:"attribution"`
	Headline    *string             `json:"headline"`
	Overview    *string             `json:"overview"`
	Sections    []periodSectionWire `json:"sections"`
}

type periodResponseWire struct {
	SchemaVersion int             `json:"schemaVersion"`
	Report        periodWire      `json:"report"`
	Notice        json.RawMessage `json:"notice"`
}

type dailyArchiveItemWire struct {
	Date          string           `json:"date"`
	GeneratedAt   string           `json:"generatedAt"`
	LeadTitle     *string          `json:"leadTitle"`
	LeadParagraph *string          `json:"leadParagraph"`
	Links         issueLinksWire   `json:"links"`
	Attribution   *attributionWire `json:"attribution"`
}

type weeklyArchiveItemWire struct {
	Week        string           `json:"week"`
	PeriodStart *string          `json:"periodStart"`
	PeriodEnd   *string          `json:"periodEnd"`
	GeneratedAt string           `json:"generatedAt"`
	Headline    *string          `json:"headline"`
	Links       issueLinksWire   `json:"links"`
	Attribution *attributionWire `json:"attribution"`
}

type monthlyArchiveItemWire struct {
	Month       string           `json:"month"`
	PeriodStart *string          `json:"periodStart"`
	PeriodEnd   *string          `json:"periodEnd"`
	GeneratedAt string           `json:"generatedAt"`
	Headline    *string          `json:"headline"`
	Links       issueLinksWire   `json:"links"`
	Attribution *attributionWire `json:"attribution"`
}

type dailyArchiveResponseWire struct {
	SchemaVersion int                    `json:"schemaVersion"`
	Count         int                    `json:"count"`
	Items         []dailyArchiveItemWire `json:"items"`
	Notice        json.RawMessage        `json:"notice"`
}

type weeklyArchiveResponseWire struct {
	SchemaVersion int                     `json:"schemaVersion"`
	Count         int                     `json:"count"`
	Items         []weeklyArchiveItemWire `json:"items"`
	Notice        json.RawMessage         `json:"notice"`
}

type monthlyArchiveResponseWire struct {
	SchemaVersion int                      `json:"schemaVersion"`
	Count         int                      `json:"count"`
	Items         []monthlyArchiveItemWire `json:"items"`
	Notice        json.RawMessage          `json:"notice"`
}

type topicLinksWire struct {
	AIHOT    string  `json:"aihot"`
	Original string  `json:"original"`
	Story    *string `json:"story"`
}

type hotTopicWire struct {
	Rank     int            `json:"rank"`
	ID       string         `json:"id"`
	Title    string         `json:"title"`
	LatestAt string         `json:"latestAt"`
	Source   sourceWire     `json:"source"`
	Links    topicLinksWire `json:"links"`
}

type hotTopicsResponseWire struct {
	SchemaVersion int             `json:"schemaVersion"`
	Count         int             `json:"count"`
	Items         []hotTopicWire  `json:"items"`
	Notice        json.RawMessage `json:"notice"`
}

type storyReportSourceWire struct {
	Name       string `json:"name"`
	FirstParty bool   `json:"firstParty"`
}

type storyReportLinksWire struct {
	AIHOT    string  `json:"aihot"`
	Original *string `json:"original"`
}

type storyReportWire struct {
	ID          string                `json:"id"`
	Title       string                `json:"title"`
	Summary     *string               `json:"summary"`
	Source      storyReportSourceWire `json:"source"`
	PublishedAt string                `json:"publishedAt"`
	Links       storyReportLinksWire  `json:"links"`
}

type storyWire struct {
	PublicID string            `json:"publicId"`
	Title    string            `json:"title"`
	Latest   string            `json:"latest"`
	Digest   *string           `json:"digest"`
	Links    issueLinksWire    `json:"links"`
	Reports  []storyReportWire `json:"reports"`
}

type storyResponseWire struct {
	SchemaVersion int             `json:"schemaVersion"`
	Story         storyWire       `json:"story"`
	Notice        json.RawMessage `json:"notice"`
}
