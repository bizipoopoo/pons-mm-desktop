package ponsmm

import (
	"context"

	"github.com/ethereum/go-ethereum/common"
)

const (
	// maxFactorySnipeExemptions is the official v2 factory's snipeTaxExemptions
	// array cap. Extra makers still run; they buy after the opening tax decays.
	maxFactorySnipeExemptions = 32
	// snipeTaxBuyLimitBps is when non-exempt makers may buy. The launch tax
	// starts near 99% and reaches 0 in ~15s; 1% is late enough to be cheap
	// without waiting out the last ticks.
	snipeTaxBuyLimitBps int64 = 100
)

func snipeExemptionCap(initialBuy bool) int {
	if initialBuy {
		return maxFactorySnipeExemptions - 1
	}
	return maxFactorySnipeExemptions
}

// selectSnipeExemptions fills the factory exemption list up to its cap and
// returns every address that is snipe-tax-free at launch (listed, or the
// treasury which the factory/router already exempts). Makers past the cap are
// omitted from the list and must wait for the tax to decay.
func selectSnipeExemptions(treasury, deployer common.Address, makers []common.Address, initialBuy bool) (list []common.Address, exempt map[common.Address]struct{}) {
	capn := snipeExemptionCap(initialBuy)
	list = make([]common.Address, 0, capn)
	exempt = map[common.Address]struct{}{treasury: {}}
	if deployer != treasury {
		list = append(list, treasury)
	}
	for _, addr := range makers {
		if len(list) >= capn {
			continue
		}
		list = append(list, addr)
		exempt[addr] = struct{}{}
	}
	return list, exempt
}

func (e *Engine) isSnipeExempt(addr common.Address) bool {
	if e.snipeExempt == nil {
		return true
	}
	_, ok := e.snipeExempt[addr]
	return ok
}

func (e *Engine) filterSnipeExempt(wallets []*Wallet) []*Wallet {
	if e.snipeExempt == nil {
		return wallets
	}
	out := make([]*Wallet, 0, len(wallets))
	for _, w := range wallets {
		if e.isSnipeExempt(w.Addr) {
			out = append(out, w)
		}
	}
	return out
}

// snipeTaxCleared is true once non-exempt makers may buy. After the tax drops
// to the limit it stays open for the rest of the run.
func (e *Engine) snipeTaxCleared(ctx context.Context) bool {
	if e.snipeExempt == nil || e.snipeTaxOpen {
		return true
	}
	var sample common.Address
	for _, w := range e.pool.Makers {
		if !e.isSnipeExempt(w.Addr) {
			sample = w.Addr
			break
		}
	}
	if sample == (common.Address{}) {
		e.snipeTaxOpen = true
		return true
	}
	if e.poolAddr == (common.Address{}) {
		return false
	}
	tax, err := e.client.SnipeTaxBps(ctx, e.poolAddr, sample)
	if err != nil {
		return false
	}
	if tax <= snipeTaxBuyLimitBps {
		e.snipeTaxOpen = true
		e.log.Info("snipe tax decayed; remaining makers will buy",
			"tax_bps", tax, "limit_bps", snipeTaxBuyLimitBps)
		return true
	}
	return false
}
