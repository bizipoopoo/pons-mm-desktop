package pons

// pons v1: tokens launch DIRECTLY into a Uniswap V3 pool against WETH (no
// bonding curve, no migration). This is the high-volume pons stack (~2-3
// launches/minute vs the v2 curve launchpad's ~2-3/hour). Addresses and event
// layout are from https://docs.ponsfamily.com/ (v1 docs) and verified on-chain.
const (
	// V1Factory deploys every v1 launch (token + locked V3 pool in one tx) and
	// emits TokenLaunched. "Active factory" in the docs, start block 8991118.
	V1Factory = "0xA5aAb3F0c6EeadF30Ef1D3Eb997108E976351feB"
	// V1SwapRouter is the official Uniswap SwapRouter02 deployment (probed
	// on-chain: factoryV2() answers, so exactInputSingle has NO deadline field).
	V1SwapRouter = "0xCaf681a66D020601342297493863E78C959E5cb2"
	// V1QuoterV2 prices swaps without executing (eth_call only).
	V1QuoterV2 = "0x33e885eD0Ec9bF04EcfB19341582aADCb4c8A9E7"
	// V1WETH is the quote token of every v1 pool.
	V1WETH = "0x0Bd7D308f8E1639FAb988df18A8011f41EAcAD73"
	// V1PoolFee is the fixed 1% fee tier of every v1 pool.
	V1PoolFee = 10000
)

const (
	v1FactoryABIJSON = `[
	{"anonymous":false,"inputs":[
		{"indexed":true,"name":"token","type":"address"},
		{"indexed":true,"name":"deployer","type":"address"},
		{"indexed":true,"name":"dexFactory","type":"address"},
		{"indexed":false,"name":"pairToken","type":"address"},
		{"indexed":false,"name":"pool","type":"address"},
		{"indexed":false,"name":"dexId","type":"uint256"},
		{"indexed":false,"name":"launchConfigId","type":"uint256"},
		{"indexed":false,"name":"positionId","type":"uint256"},
		{"indexed":false,"name":"restrictionsEndBlock","type":"uint256"},
		{"indexed":false,"name":"initialBuyAmount","type":"uint256"}],
	"name":"TokenLaunched","type":"event"},
	{"inputs":[{"name":"token","type":"address"}],"name":"getLaunchedToken","outputs":[{"components":[
		{"name":"token","type":"address"},
		{"name":"deployer","type":"address"},
		{"name":"pairedToken","type":"address"},
		{"name":"positionManager","type":"address"},
		{"name":"positionId","type":"uint256"},
		{"name":"dexId","type":"uint256"},
		{"name":"launchConfigId","type":"uint256"},
		{"name":"restrictionsEndBlock","type":"uint256"},
		{"name":"supply","type":"uint256"},
		{"name":"isToken0","type":"bool"},
		{"name":"poolFee","type":"uint24"},
		{"name":"exists","type":"bool"},
		{"name":"initialBuyAmount","type":"uint256"}],
	"name":"launched","type":"tuple"}],"stateMutability":"view","type":"function"},
	{"inputs":[{"name":"token","type":"address"}],"name":"graduationStatus","outputs":[
		{"name":"pairedPrincipal","type":"uint256"},
		{"name":"threshold","type":"uint256"},
		{"name":"graduated","type":"bool"}],"stateMutability":"view","type":"function"}
]`

	// SwapRouter02: exactInputSingle WITHOUT a deadline field (selector
	// 0x04e45aaf). Sending ETH as value with tokenIn == WETH9 makes the router
	// wrap it, so a buy needs no prior WETH balance or approval.
	v1RouterABIJSON = `[
	{"inputs":[{"components":[
		{"name":"tokenIn","type":"address"},
		{"name":"tokenOut","type":"address"},
		{"name":"fee","type":"uint24"},
		{"name":"recipient","type":"address"},
		{"name":"amountIn","type":"uint256"},
		{"name":"amountOutMinimum","type":"uint256"},
		{"name":"sqrtPriceLimitX96","type":"uint160"}],
	"name":"params","type":"tuple"}],
	"name":"exactInputSingle","outputs":[{"name":"amountOut","type":"uint256"}],"stateMutability":"payable","type":"function"}
]`

	v1QuoterABIJSON = `[
	{"inputs":[{"components":[
		{"name":"tokenIn","type":"address"},
		{"name":"tokenOut","type":"address"},
		{"name":"amountIn","type":"uint256"},
		{"name":"fee","type":"uint24"},
		{"name":"sqrtPriceLimitX96","type":"uint160"}],
	"name":"params","type":"tuple"}],
	"name":"quoteExactInputSingle","outputs":[
		{"name":"amountOut","type":"uint256"},
		{"name":"sqrtPriceX96After","type":"uint160"},
		{"name":"initializedTicksCrossed","type":"uint32"},
		{"name":"gasEstimate","type":"uint256"}],"stateMutability":"nonpayable","type":"function"}
]`

	v1WethABIJSON = `[
	{"inputs":[{"name":"wad","type":"uint256"}],"name":"withdraw","outputs":[],"stateMutability":"nonpayable","type":"function"}
]`

	// Uniswap V3 pool: only the Swap event (exit-monitor trigger).
	v1PoolABIJSON = `[
	{"anonymous":false,"inputs":[
		{"indexed":true,"name":"sender","type":"address"},
		{"indexed":true,"name":"recipient","type":"address"},
		{"indexed":false,"name":"amount0","type":"int256"},
		{"indexed":false,"name":"amount1","type":"int256"},
		{"indexed":false,"name":"sqrtPriceX96","type":"uint160"},
		{"indexed":false,"name":"liquidity","type":"uint128"},
		{"indexed":false,"name":"tick","type":"int24"}],
	"name":"Swap","type":"event"}
]`
)

var (
	v1FactoryABI = mustABI(v1FactoryABIJSON)
	v1RouterABI  = mustABI(v1RouterABIJSON)
	v1QuoterABI  = mustABI(v1QuoterABIJSON)
	v1WethABI    = mustABI(v1WethABIJSON)
	v1PoolABI    = mustABI(v1PoolABIJSON)

	v1TokenLaunchedTopic = v1FactoryABI.Events["TokenLaunched"].ID
	v1SwapTopic          = v1PoolABI.Events["Swap"].ID
)
