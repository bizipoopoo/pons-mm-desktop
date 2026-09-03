package pons

import (
	"bytes"
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

// FeePolicySnapshot is the meme-hook fee policy frozen into a launch's CREATE2
// initcode. Changing any field changes the predicted curve/token addresses.
type FeePolicySnapshot struct {
	ProtocolFeeRecipient     common.Address
	ProtocolFeeShareBps      uint16
	BuybackBurnBps           uint16
	HookFeeBps               uint16
	MaxInternalPriceImpactBps uint16
}

// V2LaunchDeployment is the exact CREATE2 input the launch deployer hashes.
// It mirrors PonsV2LaunchDeployer's LaunchDeployment struct.
type V2LaunchDeployment struct {
	PairToken           common.Address
	CreatorFeeRecipient common.Address
	OriginalDeployer    common.Address
	FeePolicy           common.Address
	Policy              FeePolicySnapshot
	FeeEscrow           common.Address
	BuybackVault        common.Address
	PhantomQuote        *big.Int
	CurveFeeBps         *big.Int
	CreatorTaxBps       *big.Int
	BuybackEnabled      bool
	GraduationThreshold *big.Int
	Supply              *big.Int
	Salt                [32]byte
	Name                string
	Symbol              string
	Logo                string
	Description         string
	Socials             V2Socials
}

// PredictV2LaunchAddresses returns the CREATE2 token and curve addresses a
// launch with these terms would deploy to, without sending a transaction.
// originalDeployer is the wallet that will call launchToken (or the router
// forwarder's declared originalDeployer for launchAndBuy).
func (c *Client) PredictV2LaunchAddresses(ctx context.Context, params V2TokenParams, launchConfigID uint64, pairToken, originalDeployer common.Address) (token, curve common.Address, err error) {
	dep, err := c.BuildV2LaunchDeployment(ctx, params, launchConfigID, pairToken, originalDeployer)
	if err != nil {
		return common.Address{}, common.Address{}, err
	}
	return c.PredictV2LaunchDeployment(ctx, dep)
}

// PredictV2LaunchDeployment calls the on-chain launch deployer view.
func (c *Client) PredictV2LaunchDeployment(ctx context.Context, dep V2LaunchDeployment) (token, curve common.Address, err error) {
	deployer, err := c.V2LaunchDeployer(ctx)
	if err != nil {
		return common.Address{}, common.Address{}, err
	}
	data, err := deployerABI.Pack("predictLaunchAddresses", dep)
	if err != nil {
		return common.Address{}, common.Address{}, fmt.Errorf("pack predictLaunchAddresses: %w", err)
	}
	res, err := c.eth.CallContract(ctx, ethereum.CallMsg{To: &deployer, Data: data}, nil)
	if err != nil {
		return common.Address{}, common.Address{}, fmt.Errorf("predictLaunchAddresses: %w", err)
	}
	out, err := deployerABI.Unpack("predictLaunchAddresses", res)
	if err != nil || len(out) < 2 {
		return common.Address{}, common.Address{}, fmt.Errorf("unpack predictLaunchAddresses: %v", err)
	}
	token = *abi.ConvertType(out[0], new(common.Address)).(*common.Address)
	curve = *abi.ConvertType(out[1], new(common.Address)).(*common.Address)
	return token, curve, nil
}

// BuildV2LaunchDeployment assembles the CREATE2 input the factory would hand
// the deployer for these launch terms, reading live config and fee policy.
func (c *Client) BuildV2LaunchDeployment(ctx context.Context, params V2TokenParams, launchConfigID uint64, pairToken, originalDeployer common.Address) (V2LaunchDeployment, error) {
	cfg, err := c.GetV2LaunchConfig(ctx, launchConfigID)
	if err != nil {
		return V2LaunchDeployment{}, err
	}
	if !cfg.Enabled {
		return V2LaunchDeployment{}, fmt.Errorf("launch config %d is disabled", launchConfigID)
	}
	hook, err := c.V2MemeHook(ctx)
	if err != nil {
		return V2LaunchDeployment{}, err
	}
	policy, err := c.CurrentFeePolicy(ctx, hook)
	if err != nil {
		return V2LaunchDeployment{}, err
	}
	escrow, err := c.V2FeeEscrow(ctx)
	if err != nil {
		return V2LaunchDeployment{}, err
	}
	vault, err := c.V2BuybackVault(ctx)
	if err != nil {
		return V2LaunchDeployment{}, err
	}
	creator := params.CreatorFeeRecipient
	if creator == (common.Address{}) {
		creator = originalDeployer
	}
	phantom, threshold := cfg.PhantomQuote, cfg.GraduationThreshold
	if pairToken != (common.Address{}) {
		return V2LaunchDeployment{}, fmt.Errorf("custom pairToken launches are not supported by PredictV2 yet")
	}
	return V2LaunchDeployment{
		PairToken:           pairToken,
		CreatorFeeRecipient: creator,
		OriginalDeployer:    originalDeployer,
		FeePolicy:           hook,
		Policy:              policy,
		FeeEscrow:           escrow,
		BuybackVault:        vault,
		PhantomQuote:        phantom,
		CurveFeeBps:         cfg.CurveFeeBps,
		CreatorTaxBps:       big.NewInt(int64(params.CreatorTaxBps)),
		BuybackEnabled:      params.BuybackEnabled,
		GraduationThreshold: threshold,
		Supply:              cfg.Supply,
		Salt:                params.Salt,
		Name:                params.Name,
		Symbol:              params.Symbol,
		Logo:                params.Logo,
		Description:         params.Description,
		Socials:             params.Socials,
	}, nil
}

func (c *Client) V2LaunchDeployer(ctx context.Context) (common.Address, error) {
	var addr common.Address
	if err := c.callView(ctx, common.HexToAddress(LaunchFactory), &factoryABI, &addr, "launchDeployer"); err != nil {
		return common.HexToAddress(LaunchDeployer), nil // fall back to documented address
	}
	if addr == (common.Address{}) {
		return common.HexToAddress(LaunchDeployer), nil
	}
	return addr, nil
}

func (c *Client) V2MemeHook(ctx context.Context) (common.Address, error) {
	var addr common.Address
	if err := c.callView(ctx, common.HexToAddress(LaunchFactory), &factoryABI, &addr, "memeHook"); err != nil {
		return common.HexToAddress(MemeHook), nil
	}
	if addr == (common.Address{}) {
		return common.HexToAddress(MemeHook), nil
	}
	return addr, nil
}

func (c *Client) V2FeeEscrow(ctx context.Context) (common.Address, error) {
	var addr common.Address
	if err := c.callView(ctx, common.HexToAddress(LaunchFactory), &factoryABI, &addr, "feeEscrow"); err != nil {
		return common.HexToAddress(FeeEscrow), nil
	}
	if addr == (common.Address{}) {
		return common.HexToAddress(FeeEscrow), nil
	}
	return addr, nil
}

func (c *Client) V2BuybackVault(ctx context.Context) (common.Address, error) {
	var addr common.Address
	if err := c.callView(ctx, common.HexToAddress(LaunchFactory), &factoryABI, &addr, "buybackVault"); err != nil {
		return common.HexToAddress(BuybackVaultAddress), nil
	}
	if addr == (common.Address{}) {
		return common.HexToAddress(BuybackVaultAddress), nil
	}
	return addr, nil
}

func (c *Client) CurrentFeePolicy(ctx context.Context, hook common.Address) (FeePolicySnapshot, error) {
	var policy FeePolicySnapshot
	data, err := memeHookABI.Pack("currentFeePolicy")
	if err != nil {
		return policy, err
	}
	res, err := c.eth.CallContract(ctx, ethereum.CallMsg{To: &hook, Data: data}, nil)
	if err != nil {
		return policy, fmt.Errorf("currentFeePolicy: %w", err)
	}
	out, err := memeHookABI.Unpack("currentFeePolicy", res)
	if err != nil || len(out) == 0 {
		return policy, fmt.Errorf("unpack currentFeePolicy: %v", err)
	}
	policy = *abi.ConvertType(out[0], new(FeePolicySnapshot)).(*FeePolicySnapshot)
	return policy, nil
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

// BuildV2LaunchAndBuy launches through the official launch-and-buy router,
// which performs the creator's first buy of quoteIn atomically inside the
// launch transaction. Nothing can trade between the launch and this buy, so it
// is immune to launch snipers by construction. For a native-ETH pair the
// transaction value must be launchFee + quoteIn.
func (s *Signer) BuildV2LaunchAndBuy(params V2TokenParams, launchConfigID uint64, pairToken common.Address, quoteIn, minTokensOut *big.Int, recipient common.Address, exemptions []common.Address, value *big.Int, p TxParams) (*types.Transaction, error) {
	data, err := routerABI.Pack("launchAndBuy", params, new(big.Int).SetUint64(launchConfigID), pairToken, quoteIn, minTokensOut, recipient, exemptions)
	if err != nil {
		return nil, fmt.Errorf("pack v2 launchAndBuy: %w", err)
	}
	return s.sign(common.HexToAddress(LaunchAndBuyRouter), value, data, p)
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

// RouterLaunchAndBuyID exposes our computed launchAndBuy selector so tools and
// tests can compare it against live router calldata.
func RouterLaunchAndBuyID() []byte { return routerABI.Methods["launchAndBuy"].ID }

// TokenLaunchedTopic exposes the TokenLaunched topic0 for external scanners.
func TokenLaunchedTopic() common.Hash { return tokenLaunchedTopic }

// RouterLaunchAndBuyCall is a decoded launchAndBuy invocation.
type RouterLaunchAndBuyCall struct {
	Params         V2TokenParams
	LaunchConfigID uint64
	PairToken      common.Address
	QuoteIn        *big.Int
	MinTokensOut   *big.Int
	Recipient      common.Address
	Exemptions     []common.Address
}

// DecodeRouterLaunchAndBuy unpacks launchAndBuy calldata with our router ABI,
// validating that the argument layout we encode matches what the router
// actually receives on-chain.
func DecodeRouterLaunchAndBuy(data []byte) (RouterLaunchAndBuyCall, error) {
	var out RouterLaunchAndBuyCall
	method := routerABI.Methods["launchAndBuy"]
	if len(data) < 4 || !bytes.Equal(data[:4], method.ID) {
		return out, fmt.Errorf("not a launchAndBuy call")
	}
	values, err := method.Inputs.Unpack(data[4:])
	if err != nil {
		return out, fmt.Errorf("unpack launchAndBuy: %w", err)
	}
	if len(values) != 7 {
		return out, fmt.Errorf("launchAndBuy expects 7 args, decoded %d", len(values))
	}
	out.Params = *abi.ConvertType(values[0], new(V2TokenParams)).(*V2TokenParams)
	out.LaunchConfigID = values[1].(*big.Int).Uint64()
	out.PairToken = values[2].(common.Address)
	out.QuoteIn = values[3].(*big.Int)
	out.MinTokensOut = values[4].(*big.Int)
	out.Recipient = values[5].(common.Address)
	out.Exemptions = values[6].([]common.Address)
	return out, nil
}

// V2AtomicBuyFromReceipt returns the tokensOut of the CurveBuy that the
// launch-and-buy router executed for recipient inside a launch receipt.
func V2AtomicBuyFromReceipt(rcpt *types.Receipt, curve, recipient common.Address) (*big.Int, bool) {
	for _, lg := range rcpt.Logs {
		if lg.Address != curve || len(lg.Topics) < 3 || lg.Topics[0] != curveBuyTopic {
			continue
		}
		if common.BytesToAddress(lg.Topics[2].Bytes()) != recipient {
			continue
		}
		var data struct {
			QuoteIn   *big.Int
			TokensOut *big.Int
			Fee       *big.Int
			Tax       *big.Int
		}
		if err := curveABI.UnpackIntoInterface(&data, "CurveBuy", lg.Data); err != nil {
			continue
		}
		return data.TokensOut, true
	}
	return nil, false
}
