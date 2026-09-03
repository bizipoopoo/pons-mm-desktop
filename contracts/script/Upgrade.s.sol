// SPDX-License-Identifier: MIT
pragma solidity ^0.8.28;

import {Script, console} from "forge-std/Script.sol";
import {PonsMMRouter} from "../src/PonsMMRouter.sol";

/// @notice Deploys a new implementation and points the existing proxy at it.
/// Run with DEPLOYER_KEY (the proxy owner) and ROUTER_PROXY set.
contract Upgrade is Script {
    function run() external {
        uint256 key = vm.envUint("DEPLOYER_KEY");
        address proxy = vm.envAddress("ROUTER_PROXY");
        vm.startBroadcast(key);
        PonsMMRouter logic = new PonsMMRouter();
        PonsMMRouter(payable(proxy)).upgradeToAndCall(address(logic), "");
        vm.stopBroadcast();
        console.log("new implementation:", address(logic));
    }
}
