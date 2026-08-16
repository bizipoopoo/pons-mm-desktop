package ponsmm

import (
	"encoding/json"
	"fmt"
	"os"
)

// gmgn wallet tagging.
//
// gmgn.ai's "标记 / followed wallet" list is a per-account watchlist of
// {address, name, emoji} entries (max 2000), populated through the website —
// either one by one or via the bulk-import box. gmgn exposes NO public or
// official API to ADD or remark a wallet: its documented API and CLI only READ
// the follow list, and the private website endpoints are login-gated and
// Cloudflare-protected. So we cannot reliably log in from Go and tag wallets
// without reverse-engineering a bot-protected endpoint that would break.
//
// What we CAN do reliably is produce the exact bulk-import payload gmgn accepts.
// Re-importing the same list is idempotent. Viewer credentials deliberately do
// not live in source code or in this generated file.

// GmgnMark is one entry in gmgn's bulk-import / export wallet-list format.
type GmgnMark struct {
	Address string `json:"address"`
	Name    string `json:"name"`
	Emoji   string `json:"emoji"`
}

// BuildGmgnImport turns the wallet pool into gmgn bulk-import entries. The
// treasury/deployer is labeled distinctly; makers are numbered. namePrefix and
// emoji default to "PONSMM" and "🤖" when empty.
func BuildGmgnImport(pool *Pool, namePrefix, emoji string) []GmgnMark {
	if namePrefix == "" {
		namePrefix = "PONSMM"
	}
	if emoji == "" {
		emoji = "🤖"
	}
	marks := make([]GmgnMark, 0, len(pool.Makers)+1)
	if pool.Treasury != nil {
		marks = append(marks, GmgnMark{
			Address: pool.Treasury.Addr.Hex(),
			Name:    namePrefix + "-deployer",
			Emoji:   emoji,
		})
	}
	for i, w := range pool.Makers {
		marks = append(marks, GmgnMark{
			Address: w.Addr.Hex(),
			Name:    fmt.Sprintf("%s-%02d", namePrefix, i+1),
			Emoji:   emoji,
		})
	}
	return marks
}

// WriteGmgnImport writes marks as gmgn's bulk-import JSON (pretty-printed) to
// path. Returns the number of entries written.
func WriteGmgnImport(path string, marks []GmgnMark) (int, error) {
	b, err := json.MarshalIndent(marks, "", "  ")
	if err != nil {
		return 0, fmt.Errorf("marshal gmgn import: %w", err)
	}
	b = append(b, '\n')
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return 0, fmt.Errorf("write %s: %w", path, err)
	}
	return len(marks), nil
}
