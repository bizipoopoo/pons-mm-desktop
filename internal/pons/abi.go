package pons

import (
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
)

// Deployed pons v2 contract addresses on Robinhood Chain (chain id 4663).
// Bonding curves and launch tokens are created per launch and resolved from the
// factory, never hardcoded.
const (
	// LaunchFactory is the CURRENT pons v2 launchpad factory: it deploys every
	// launch and emits TokenLaunched. pons replaces the whole stack on upgrade
	// (contracts are immutable), so this address changes over time — the
	// authoritative list is https://docs.ponsfamily.com/v2 under Contracts.
	// The previous deployment 0x7E1EAbd52Ae29598e6483F72dCf1a70b14284dB8 went
	// quiet once this one took over.
	LaunchFactory = "0x7eD598BcEf8bd9Edd8C97A195C6d13f40801EC7e"
	// FeeEscrow holds claimable protocol/creator balances (unused by the
	// sniper, kept for reference).
	FeeEscrow = "0xd3AFEB2a57f70eF218Aa82451c51B2fb0416Ac9e"

	// RobinhoodChainID is the Robinhood Chain mainnet chain id.
	RobinhoodChainID = 4663
	// DefaultRPC is the public (rate-limited) Robinhood Chain RPC. Use a
	// dedicated provider for production sniping.
	DefaultRPC = "https://rpc.mainnet.chain.robinhood.com"

	// NativeQuote is the sentinel pairToken for an ETH-quoted launch.
	NativeQuote = "0x0000000000000000000000000000000000000000"
)

// Minimal ABIs: only the members the sniper reads or calls.
const (
	factoryABIJSON = `[
	{"anonymous":false,"inputs":[
		{"indexed":true,"name":"token","type":"address"},
		{"indexed":true,"name":"curve","type":"address"},
		{"indexed":true,"name":"deployer","type":"address"},
		{"indexed":false,"name":"pairToken","type":"address"},
		{"indexed":false,"name":"launchConfigId","type":"uint256"},
		{"indexed":false,"name":"graduationThreshold","type":"uint256"}],
	"name":"TokenLaunched","type":"event"}
]`

	curveABIJSON = `[
	{"inputs":[{"name":"quoteIn","type":"uint256"},{"name":"minTokensOut","type":"uint256"},{"name":"recipient","type":"address"}],"name":"buy","outputs":[{"name":"tokensOut","type":"uint256"}],"stateMutability":"payable","type":"function"},
	{"inputs":[{"name":"tokensIn","type":"uint256"},{"name":"minQuoteOut","type":"uint256"},{"name":"recipient","type":"address"}],"name":"sell","outputs":[{"name":"quoteOut","type":"uint256"}],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[],"name":"getReserves","outputs":[{"name":"quoteReserve","type":"uint256"},{"name":"tokenReserve","type":"uint256"}],"stateMutability":"view","type":"function"},
	{"inputs":[],"name":"realQuoteReserve","outputs":[{"name":"","type":"uint256"}],"stateMutability":"view","type":"function"},
	{"inputs":[],"name":"graduationThreshold","outputs":[{"name":"","type":"uint256"}],"stateMutability":"view","type":"function"},
	{"inputs":[],"name":"sellableTokens","outputs":[{"name":"","type":"uint256"}],"stateMutability":"view","type":"function"},
	{"inputs":[],"name":"readyToGraduate","outputs":[{"name":"","type":"bool"}],"stateMutability":"view","type":"function"},
	{"inputs":[],"name":"graduated","outputs":[{"name":"","type":"bool"}],"stateMutability":"view","type":"function"},
	{"inputs":[],"name":"isNativeQuote","outputs":[{"name":"","type":"bool"}],"stateMutability":"view","type":"function"},
	{"inputs":[],"name":"pairToken","outputs":[{"name":"","type":"address"}],"stateMutability":"view","type":"function"},
	{"inputs":[],"name":"feeBps","outputs":[{"name":"","type":"uint256"}],"stateMutability":"view","type":"function"},
	{"inputs":[],"name":"creatorTaxBps","outputs":[{"name":"","type":"uint256"}],"stateMutability":"view","type":"function"},
	{"inputs":[{"name":"recipient","type":"address"}],"name":"currentSnipeTaxBps","outputs":[{"name":"","type":"uint256"}],"stateMutability":"view","type":"function"},
	{"anonymous":false,"inputs":[
		{"indexed":true,"name":"buyer","type":"address"},
		{"indexed":true,"name":"recipient","type":"address"},
		{"indexed":false,"name":"quoteIn","type":"uint256"},
		{"indexed":false,"name":"tokensOut","type":"uint256"},
		{"indexed":false,"name":"fee","type":"uint256"},
		{"indexed":false,"name":"tax","type":"uint256"}],
	"name":"CurveBuy","type":"event"},
	{"anonymous":false,"inputs":[
		{"indexed":true,"name":"seller","type":"address"},
		{"indexed":true,"name":"recipient","type":"address"},
		{"indexed":false,"name":"tokensIn","type":"uint256"},
		{"indexed":false,"name":"quoteOut","type":"uint256"},
		{"indexed":false,"name":"fee","type":"uint256"},
		{"indexed":false,"name":"tax","type":"uint256"}],
	"name":"CurveSell","type":"event"}
]`

	erc20ABIJSON = `[
	{"inputs":[],"name":"name","outputs":[{"name":"","type":"string"}],"stateMutability":"view","type":"function"},
	{"inputs":[],"name":"symbol","outputs":[{"name":"","type":"string"}],"stateMutability":"view","type":"function"},
	{"inputs":[],"name":"decimals","outputs":[{"name":"","type":"uint8"}],"stateMutability":"view","type":"function"},
	{"inputs":[{"name":"account","type":"address"}],"name":"balanceOf","outputs":[{"name":"","type":"uint256"}],"stateMutability":"view","type":"function"},
	{"inputs":[{"name":"spender","type":"address"},{"name":"amount","type":"uint256"}],"name":"approve","outputs":[{"name":"","type":"bool"}],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"owner","type":"address"},{"name":"spender","type":"address"}],"name":"allowance","outputs":[{"name":"","type":"uint256"}],"stateMutability":"view","type":"function"}
]`
)

var (
	factoryABI = mustABI(factoryABIJSON)
	curveABI   = mustABI(curveABIJSON)
	erc20ABI   = mustABI(erc20ABIJSON)

	// tokenLaunchedTopic is the keccak topic0 of TokenLaunched, used to filter
	// factory logs.
	tokenLaunchedTopic = factoryABI.Events["TokenLaunched"].ID
	curveBuyTopic      = curveABI.Events["CurveBuy"].ID
	curveSellTopic     = curveABI.Events["CurveSell"].ID
)

func mustABI(js string) abi.ABI {
	a, err := abi.JSON(strings.NewReader(js))
	if err != nil {
		panic("pons: bad ABI: " + err.Error())
	}
	return a
}
