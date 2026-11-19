package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// discordChannel pushes to a Discord incoming webhook: rich embeds with
// severity colors, chain-type indication and asset/summary fields. One channel
// instance = one webhook URL; the three-notification-room pattern (global
// activity / primitives / findings) is three channel instances with different
// event filters.
type discordChannel struct{}

func (discordChannel) Kind() string { return KindDiscord }

// Discord's real per-webhook limit is ~30/min with the 5/2s bucket; 20 leaves
// headroom for bursts.
func (discordChannel) DefaultRatePerMin() int { return 20 }

// The webhook URL carries the token; mask it in API responses.
func (discordChannel) SecretKeys() []string { return []string{"url"} }

func (discordChannel) DestinationKeys() []string { return []string{"url"} }

// severity colors (decimal ints per Discord embed spec)
var discordSeverityColor = map[string]int{
	"critical": 0xED4245, // red
	"high":     0xE67E22, // orange
	"medium":   0xF1C40F, // yellow
	"low":      0x3498DB, // blue
}

const (
	discordColorInfo    = 0x5865F2 // blurple — activity / status events
	discordColorPrim    = 0x99AAB5 // grey — primitives
	discordColorReclass = 0x9B59B6 // purple — reclassification
)

// discordSend is the POST body of a Discord webhook with embeds.
type discordSend struct {
	Embeds []discordEmbed `json:"embeds"`
}

type discordEmbed struct {
	Title       string              `json:"title"`
	Description string              `json:"description,omitempty"`
	Color       int                 `json:"color,omitempty"`
	URL         string              `json:"url,omitempty"`
	Fields      []discordEmbedField `json:"fields,omitempty"`
	Footer      discordEmbedFooter  `json:"footer,omitempty"`
}

type discordEmbedField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline,omitempty"`
}

type discordEmbedFooter struct {
	Text string `json:"text"`
}

// Send delivers one message as one embed (batch digests become one embed with
// the items in the description).
func (c discordChannel) Send(ctx context.Context, cfg map[string]any, msg Message) (int, error) {
	url, _ := cfg["url"].(string)
	if strings.TrimSpace(url) == "" {
		return 0, fmt.Errorf("discord webhook url is not configured")
	}
	send := discordSend{Embeds: []discordEmbed{c.buildEmbed(msg)}}
	body, err := json.Marshal(send)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("discord webhook: %s", resp.Status)
	}
	return resp.StatusCode, nil
}

func (discordChannel) buildEmbed(msg Message) discordEmbed {
	lim := msg.limits()
	e := discordEmbed{Footer: discordEmbedFooter{Text: "ARTEX"}}
	if msg.HomeURL != "" {
		e.URL = msg.HomeURL
	}

	if msg.Batch {
		e.Title = fmt.Sprintf("Digest · %d findings in the last %d min", len(msg.Items), msg.WindowMinutes)
		e.Color = discordSeverityColor["low"]
		var lines []string
		for _, it := range msg.Items {
			lines = append(lines, fmt.Sprintf("- **%s** · `%s` · %s", truncateRunes(it.Title(), 80), it.VulnClass, it.Severity))
		}
		e.Description = truncateRunes(strings.Join(lines, "\n"), 3500)
		return e
	}

	it := msg.Items[0]
	e.Title = truncateRunes(it.Title(), 240)
	e.Color = discordSeverityColor[it.Severity]
	if e.Color == 0 {
		switch it.Event {
		case EventPrimitiveFound:
			e.Color = discordColorPrim
		case EventFindingReclassified:
			e.Color = discordColorReclass
		default:
			e.Color = discordColorInfo
		}
	}
	if it.DetailURL != "" {
		e.URL = it.DetailURL
	}
	e.Description = truncateRunes(it.Summary, 1000)

	var fields []discordEmbedField
	fields = append(fields, discordEmbedField{Name: "Class", Value: "`" + it.VulnClass + "`", Inline: true})
	fields = append(fields, discordEmbedField{Name: "Severity", Value: it.Severity, Inline: true})
	// chain-type badge on every finding event
	switch {
	case it.ChainType == "exploit-chain":
		fields = append(fields, discordEmbedField{Name: "Type", Value: "🟢 exploit chain (confirmed)", Inline: true})
	case it.ChainType == "hypothesis":
		fields = append(fields, discordEmbedField{Name: "Type", Value: "🟡 hypothesis (assumed steps)", Inline: true})
	case it.ChainType == "primitives":
		fields = append(fields, discordEmbedField{Name: "Type", Value: "⚪ primitives only", Inline: true})
	}
	if it.IsStatusChange() {
		fields = append(fields, discordEmbedField{Name: "Status", Value: it.FromStatus + " → " + it.ToStatus, Inline: true})
	}
	if it.IsReclassified() {
		fields = append(fields, discordEmbedField{Name: "Reclassified",
			Value: fmt.Sprintf("`%s` → `%s`", it.FromVulnClass, it.ToVulnClass), Inline: true})
		if it.ReclassReason != "" {
			fields = append(fields, discordEmbedField{Name: "Reason", Value: truncateRunes(it.ReclassReason, 900)})
		}
	}
	if len(it.Primitives) > 0 {
		var lines []string
		for _, p := range it.Primitives {
			mark := "◦"
			if p.Confirmed {
				mark = "✔"
			}
			lines = append(lines, fmt.Sprintf("%s %s", mark, truncateRunes(p.Name, 90)))
		}
		fields = append(fields, discordEmbedField{Name: fmt.Sprintf("Primitives (%d)", len(it.Primitives)),
			Value: truncateRunes(strings.Join(lines, "\n"), 900)})
	}
	if len(it.Assets) > 0 {
		shown := it.Assets
		if lim.AssetsShown > 0 && len(shown) > lim.AssetsShown {
			shown = append(append([]string{}, shown[:lim.AssetsShown]...), fmt.Sprintf("+%d more", len(it.Assets)-lim.AssetsShown))
		}
		fields = append(fields, discordEmbedField{Name: "Assets", Value: truncateRunes(strings.Join(shown, ", "), 900)})
	}
	e.Fields = fields
	return e
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// Validate requires the webhook URL and rejects obvious non-Discord endpoints
// early (typo detection, not security).
func (discordChannel) Validate(cfg map[string]any) error {
	url, _ := cfg["url"].(string)
	u := strings.TrimSpace(url)
	if u == "" {
		return fmt.Errorf("discord webhook url is required")
	}
	if !strings.HasPrefix(u, "https://discord.com/api/webhooks/") && !strings.HasPrefix(u, "https://discordapp.com/api/webhooks/") {
		return fmt.Errorf("url must be a Discord incoming webhook (https://discord.com/api/webhooks/...)")
	}
	return nil
}
