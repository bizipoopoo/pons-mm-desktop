// SPDX-License-Identifier: MIT
pragma solidity ^0.8.28;

import {Script, console} from "forge-std/Script.sol";
import {PonsBalanceLens} from "../src/PonsBalanceLens.sol";

/// @notice Deploys the balance lens. The desktop app does not require this:
/// it injects the same bytecode via eth_call state override. Deploying is
/// useful if an RPC rejects state overrides.
contract DeployBalanceLens is Script {
    function run() external {
        uint256 key = vm.envUint("DEPLOYER_KEY");
        vm.startBroadcast(key);
        PonsBalanceLens lens = new PonsBalanceLens();
        vm.stopBroadcast();
        console.log("PonsBalanceLens:", address(lens));
    }
}
