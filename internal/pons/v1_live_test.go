package pons

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// TestLiveReadV1Launch is an opt-in read-only check against a real pons v1
// launch: factory record decode, quoter pricing both ways, and the L1 block
// number read that gates the launch-protection wait.
//
//	PUMPWATCH_LIVE=1 PONS_V1_TOKEN=0x... go test ./internal/pons -run TestLiveReadV1Launch -v
func TestLiveReadV1Launch(t *testing.T) {
	if os.Getenv("PUMPWATCH_LIVE") == "" {
		t.Skip("set PUMPWATCH_LIVE=1 to run the live read test")
	}
	token := os.Getenv("PONS_V1_TOKEN")
	if token == "" {
		t.Skip("set PONS_V1_TOKEN to a v1-launched token")
	}
	rpcURL := os.Getenv("PONS_RPC")
	if rpcURL == "" {
		rpcURL = DefaultRPC
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	c, err := Dial(ctx, rpcURL)
	if err != nil {
		t.Fatal(err)
	}

	st, err := c.GetV1Launch(ctx, common.HexToAddress(token))
	if err != nil {
		t.Fatalf("GetV1Launch: %v", err)
	}
	if st.PairedToken != common.HexToAddress(V1WETH) {
		t.Fatalf("paired token %s is not WETH", st.PairedToken.Hex())
	}
	if st.PoolFee.Int64() != V1PoolFee {
		t.Fatalf("pool fee %s, want %d", st.PoolFee, V1PoolFee)
	}
	meta := c.LoadV1TokenMeta(ctx, st.Token)
	t.Logf("symbol=%q name=%q deployer=%s initialBuy=%s ETH restrictionsEnd(L1)=%s",
		meta.Symbol, meta.Name, st.Deployer.Hex(), weiToEth(st.InitialBuyAmount), st.RestrictionsEndBlock)

	l1bn, err := c.L1BlockNumber(ctx)
	if err != nil {
		t.Fatalf("L1BlockNumber: %v", err)
	}
	if l1bn == 0 {
		t.Fatal("l1 block number is zero; header missing l1BlockNumber field")
	}
	t.Logf("current L1 block=%d (restrictions %s)", l1bn,
		map[bool]string{true: "over", false: "ACTIVE"}[l1bn > st.RestrictionsEndBlock.Uint64()])

	spend := ethToWei(0.01)
	tokensOut, err := c.QuoteV1Buy(ctx, st.Token, spend)
	if err != nil {
		t.Fatalf("QuoteV1Buy: %v", err)
	}
	if tokensOut.Sign() <= 0 {
		t.Fatal("buy quote returned zero tokens")
	}
	back, err := c.QuoteV1Sell(ctx, st.Token, tokensOut)
	if err != nil {
		t.Fatalf("QuoteV1Sell: %v", err)
	}
	t.Logf("0.01 ETH buys %s base units; immediate sell-back returns %s ETH", weiToStr(tokensOut), weiToEth(back))
	if back.Cmp(spend) >= 0 {
		t.Fatal("round-trip returned >= spent; fee math or quoter wiring is wrong")
	}
}
