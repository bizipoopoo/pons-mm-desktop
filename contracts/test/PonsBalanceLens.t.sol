// SPDX-License-Identifier: MIT
pragma solidity ^0.8.28;

import {Test} from "forge-std/Test.sol";
import {PonsBalanceLens} from "../src/PonsBalanceLens.sol";

contract MockERC20 {
    mapping(address => uint256) internal _bal;
    bool public boom;

    function set(address who, uint256 v) external {
        _bal[who] = v;
    }

    function setBoom(bool v) external {
        boom = v;
    }

    function balanceOf(address who) public view returns (uint256) {
        if (boom) revert("nope");
        return _bal[who];
    }
}

contract PonsBalanceLensTest is Test {
    PonsBalanceLens lens;
    MockERC20 token;

    address a = address(0xA11CE);
    address b = address(0xB0B);

    function setUp() public {
        lens = new PonsBalanceLens();
        token = new MockERC20();
        vm.deal(a, 1 ether);
        vm.deal(b, 2.5 ether);
        token.set(a, 11);
        token.set(b, 22);
    }

    function testEthBalances() public view {
        address[] memory accounts = new address[](2);
        accounts[0] = a;
        accounts[1] = b;
        uint256[] memory out = lens.ethBalances(accounts);
        assertEq(out.length, 2);
        assertEq(out[0], 1 ether);
        assertEq(out[1], 2.5 ether);
    }

    function testTokenBalances() public view {
        address[] memory accounts = new address[](2);
        accounts[0] = a;
        accounts[1] = b;
        uint256[] memory out = lens.tokenBalances(address(token), accounts);
        assertEq(out[0], 11);
        assertEq(out[1], 22);
    }

    function testSnapshotBoth() public view {
        address[] memory accounts = new address[](2);
        accounts[0] = a;
        accounts[1] = b;
        (uint256[] memory native, uint256[] memory tokens) = lens.snapshot(address(token), accounts);
        assertEq(native[0], 1 ether);
        assertEq(native[1], 2.5 ether);
        assertEq(tokens[0], 11);
        assertEq(tokens[1], 22);
    }

    function testSnapshotSkipsTokenWhenZero() public view {
        address[] memory accounts = new address[](1);
        accounts[0] = a;
        (uint256[] memory native, uint256[] memory tokens) = lens.snapshot(address(0), accounts);
        assertEq(native[0], 1 ether);
        assertEq(tokens[0], 0);
    }

    function testTokenRevertYieldsZero() public {
        token.setBoom(true);
        address[] memory accounts = new address[](1);
        accounts[0] = a;
        uint256[] memory out = lens.tokenBalances(address(token), accounts);
        assertEq(out[0], 0);
    }

    function testEmpty() public view {
        address[] memory accounts = new address[](0);
        uint256[] memory out = lens.ethBalances(accounts);
        assertEq(out.length, 0);
    }
}
