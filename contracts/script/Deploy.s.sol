// SPDX-License-Identifier: MIT
pragma solidity ^0.8.28;

import {Script, console} from "forge-std/Script.sol";
import {ERC1967Proxy} from "@openzeppelin/contracts/proxy/ERC1967/ERC1967Proxy.sol";
import {PonsMMRouter} from "../src/PonsMMRouter.sol";

/// @notice Deploys the router implementation behind a fresh UUPS proxy owned
/// by the broadcasting key. Run with DEPLOYER_KEY set.
contract Deploy is Script {
    function run() external {
        uint256 key = vm.envUint("DEPLOYER_KEY");
        address owner = vm.addr(key);
        vm.startBroadcast(key);
        PonsMMRouter logic = new PonsMMRouter();
        ERC1967Proxy proxy = new ERC1967Proxy(address(logic), abi.encodeCall(PonsMMRouter.initialize, (owner)));
        vm.stopBroadcast();
        console.log("PonsMMRouter implementation:", address(logic));
        console.log("PonsMMRouter proxy:", address(proxy));
        console.log("owner:", owner);
    }
}
