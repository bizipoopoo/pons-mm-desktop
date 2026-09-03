# pons-mm contracts

`PonsMMRouter` is the desktop app's own block-height-limited buy router for
pons v2 bonding curves. Maker buys that are bundled with a launch go through
`buyWithin(curve, maxL2Block, recipient)`, which reverts with `Expired` once
`ArbSys.arbBlockNumber()` passes `maxL2Block`, so a buy that misses its
window never fills late at a worse price. Slippage is not enforced: the
window is the only limit, exactly as the launch bundle wants.

The router is a UUPS proxy so new entrypoints (for example a limited sell)
can be added without changing the address the app is configured with.

## Setup

Dependencies are not vendored. After cloning:

```sh
curl -L https://foundry.paradigm.xyz | bash && foundryup
cd contracts
forge install OpenZeppelin/openzeppelin-contracts@v5.4.0 --no-git
forge install OpenZeppelin/openzeppelin-contracts-upgradeable@v5.4.0 --no-git
forge install foundry-rs/forge-std --no-git
forge test
```

## Deploy

```sh
export ROBINHOOD_RPC=https://...
export DEPLOYER_KEY=0x...
forge script script/Deploy.s.sol --rpc-url robinhood --broadcast
```

The script prints the proxy address; that address is what the app's
Settings page (`Block-limited router contract`) and `internal/pons/abi.go`
(`MMRouter`) point at. Upgrades use `script/Upgrade.s.sol` with the same
owner key and `ROUTER_PROXY` set to the proxy address.
