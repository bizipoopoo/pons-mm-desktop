package pons

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// V2Socials mirrors the pons v2 factory Socials tuple.
type V2Socials struct {
	Twitter   string
	Telegram  string
	Discord   string
	Website   string
	Farcaster string
}

// V2TokenParams is the complete v2 launchToken parameter tuple. The engine
// pins ExpectedEconomics immediately before building the transaction so an
// owner-side config change cannot silently alter launch terms.
type V2TokenParams struct {
	Name                string
	Symbol              string
	Logo                string
	Description         string
	Socials             V2Socials
	CreatorFeeRecipient common.Address
	CreatorTaxBps       uint16
	BuybackEnabled      bool
	ExpectedEconomics   [32]byte
	Salt                [32]byte
}

// V2LaunchConfig is one owner-managed bonding-curve launch configuration.
type V2LaunchConfig struct {
	Supply              *big.Int
	CurveFeeBps         *big.Int
	PhantomQuote        *big.Int
	GraduationThreshold *big.Int
	PoolFee             *big.Int
	TickSpacing         *big.Int
	Enabled             bool
}

func (c *Client) CanLaunchV2(ctx context.Context, who common.Address) (bool, error) {
	var can bool
	if err := c.callView(ctx, common.HexToAddress(LaunchFactory), &factoryABI, &can, "canLaunch", who); err != nil {
		return false, err
	}
	return can, nil
}

func (c *Client) V2LaunchFee(ctx context.Context) (*big.Int, error) {
	var fee *big.Int
	if err := c.callView(ctx, common.HexToAddress(LaunchFactory), &factoryABI, &fee, "launchFee"); err != nil {
		return nil, err
	}
	return fee, nil
}

func (c *Client) GetV2LaunchConfig(ctx context.Context, id uint64) (V2LaunchConfig, error) {
	var cfg V2LaunchConfig
	data, err := factoryABI.Pack("getLaunchConfig", new(big.Int).SetUint64(id))
	if err != nil {
		return cfg, fmt.Errorf("pack getLaunchConfig: %w", err)
	}
	to := common.HexToAddress(LaunchFactory)
	res, err := c.eth.CallContract(ctx, ethereum.CallMsg{To: &to, Data: data}, nil)
	if err != nil {
		return cfg, fmt.Errorf("call getLaunchConfig: %w", err)
	}
	out, err := factoryABI.Unpack("getLaunchConfig", res)
	if err != nil || len(out) == 0 {
		return cfg, fmt.Errorf("unpack getLaunchConfig: %v", err)
	}
	cfg = *abi.ConvertType(out[0], new(V2LaunchConfig)).(*V2LaunchConfig)
	return cfg, nil
}

func (c *Client) PreviewV2LaunchEconomics(ctx context.Context, launchConfigID uint64, pairToken common.Address) ([32]byte, error) {
	var economics [32]byte
	if err := c.callView(ctx, common.HexToAddress(LaunchFactory), &factoryABI, &economics,
		"previewLaunchEconomics", new(big.Int).SetUint64(launchConfigID), pairToken); err != nil {
		return [32]byte{}, err
	}
	return economics, nil
}

func (s *Signer) BuildV2Launch(params V2TokenParams, launchConfigID uint64, pairToken common.Address, exemptions []common.Address, value *big.Int, p TxParams) (*types.Transaction, error) {
	data, err := factoryABI.Pack("launchToken", params, new(big.Int).SetUint64(launchConfigID), pairToken, exemptions)
	if err != nil {
		return nil, fmt.Errorf("pack v2 launchToken: %w", err)
	}
	return s.sign(common.HexToAddress(LaunchFactory), value, data, p)
}

// V2LaunchedFromReceipt extracts the v2 token + bonding curve from a launch
// receipt. It accepts only logs emitted by the current v2 factory.
func V2LaunchedFromReceipt(rcpt *types.Receipt) (Launch, bool) {
	factory := common.HexToAddress(LaunchFactory)
	for _, lg := range rcpt.Logs {
		if lg.Address != factory {
			continue
		}
		if launched, ok := decodeLaunch(*lg); ok {
			return launched, true
		}
	}
	return Launch{}, false
}
