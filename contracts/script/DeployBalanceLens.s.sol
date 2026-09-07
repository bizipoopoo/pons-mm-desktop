// SPDX-License-Identifier: MIT
pragma solidity ^0.8.28;

import {Script, console} from "forge-std/Script.sol";
import {PonsBalanceLens} from "../src/PonsBalanceLens.sol";

/// @notice Deploys the balance lens. The desktop app calls the mainnet
/// deployment at 0x48Fb8011E2E7a2eF3dCfe00105E3cA2869E909dc; this script is
/// for a new chain or a replacement.
contract DeployBalanceLens is Script {
    function run() external {
        uint256 key = vm.envUint("DEPLOYER_KEY");
        vm.startBroadcast(key);
        PonsBalanceLens lens = new PonsBalanceLens();
        vm.stopBroadcast();
        console.log("PonsBalanceLens:", address(lens));
    }
}
