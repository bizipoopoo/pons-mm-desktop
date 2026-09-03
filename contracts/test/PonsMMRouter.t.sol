// SPDX-License-Identifier: MIT
pragma solidity ^0.8.28;

import {Test} from "forge-std/Test.sol";
import {ERC1967Proxy} from "@openzeppelin/contracts/proxy/ERC1967/ERC1967Proxy.sol";
import {OwnableUpgradeable} from "@openzeppelin/contracts-upgradeable/access/OwnableUpgradeable.sol";
import {PonsMMRouter} from "../src/PonsMMRouter.sol";

contract MockArbSys {
    uint256 public height;

    function set(uint256 h) external {
        height = h;
    }

    function arbBlockNumber() external view returns (uint256) {
        return height;
    }
}

/// @dev Curve stand-in: sells `rate` tokens per wei, refunds anything above
/// `capWei`, and records the arguments it was called with.
contract MockCurve {
    uint256 public rate = 1000;
    uint256 public capWei = type(uint256).max;
    address public lastRecipient;
    uint256 public lastQuoteIn;
    uint256 public lastMinOut;
    address public lastSender;

    function setCap(uint256 c) external {
        capWei = c;
    }

    function buy(uint256 quoteIn, uint256 minTokensOut, address recipient) external payable returns (uint256) {
        require(msg.value == quoteIn, "value mismatch");
        lastRecipient = recipient;
        lastQuoteIn = quoteIn;
        lastMinOut = minTokensOut;
        lastSender = msg.sender;
        uint256 spent = quoteIn > capWei ? capWei : quoteIn;
        uint256 refund = quoteIn - spent;
        if (refund != 0) {
            (bool ok,) = payable(msg.sender).call{value: refund}("");
            require(ok, "refund");
        }
        return spent * rate;
    }
}

contract PonsMMRouterV2Mock is PonsMMRouter {
    function version() external pure returns (uint256) {
        return 2;
    }
}

contract PonsMMRouterTest is Test {
    MockArbSys arbSys;
    MockCurve curve;
    PonsMMRouter router;
    address owner = makeAddr("owner");
    address maker = makeAddr("maker");

    function setUp() public {
        // ArbSys lives at address(100) on every Arbitrum chain.
        MockArbSys impl = new MockArbSys();
        vm.etch(address(100), address(impl).code);
        arbSys = MockArbSys(address(100));
        arbSys.set(1_000_000);

        curve = new MockCurve();
        PonsMMRouter logic = new PonsMMRouter();
        bytes memory init = abi.encodeCall(PonsMMRouter.initialize, (owner));
        router = PonsMMRouter(payable(address(new ERC1967Proxy(address(logic), init))));
        vm.deal(maker, 10 ether);
    }

    function test_buyInsideWindow() public {
        vm.prank(maker);
        uint256 out = router.buyWithin{value: 1 ether}(address(curve), 1_000_002, maker);
        assertEq(out, 1 ether * 1000);
        assertEq(curve.lastRecipient(), maker);
        assertEq(curve.lastQuoteIn(), 1 ether);
        assertEq(curve.lastMinOut(), 0, "no slippage bound");
        assertEq(curve.lastSender(), address(router));
        assertEq(address(router).balance, 0);
    }

    function test_buyAtExactMaxBlock() public {
        arbSys.set(1_000_003);
        vm.prank(maker);
        router.buyWithin{value: 1 ether}(address(curve), 1_000_003, maker);
    }

    function test_revertsPastWindow() public {
        arbSys.set(1_000_004);
        vm.prank(maker);
        vm.expectRevert(abi.encodeWithSelector(PonsMMRouter.Expired.selector, 1_000_004, 1_000_003));
        router.buyWithin{value: 1 ether}(address(curve), 1_000_003, maker);
        assertEq(maker.balance, 10 ether, "reverted buy costs nothing but gas");
    }

    function test_refundForwardedToCaller() public {
        curve.setCap(0.25 ether);
        uint256 before = maker.balance;
        vm.prank(maker);
        uint256 out = router.buyWithin{value: 1 ether}(address(curve), 1_000_001, maker);
        assertEq(out, 0.25 ether * 1000);
        assertEq(before - maker.balance, 0.25 ether, "only the filled part is spent");
        assertEq(address(router).balance, 0, "router keeps nothing");
    }

    function test_rejectsZeroValue() public {
        vm.prank(maker);
        vm.expectRevert(PonsMMRouter.ZeroValue.selector);
        router.buyWithin(address(curve), 1_000_001, maker);
    }

    function test_currentL2Block() public view {
        assertEq(router.currentL2Block(), 1_000_000);
    }

    function test_upgradeOnlyOwner() public {
        PonsMMRouterV2Mock v2 = new PonsMMRouterV2Mock();
        vm.prank(maker);
        vm.expectRevert(abi.encodeWithSelector(OwnableUpgradeable.OwnableUnauthorizedAccount.selector, maker));
        router.upgradeToAndCall(address(v2), "");

        vm.prank(owner);
        router.upgradeToAndCall(address(v2), "");
        assertEq(PonsMMRouterV2Mock(payable(address(router))).version(), 2);
        assertEq(router.owner(), owner, "state survives upgrade");
    }

    function test_cannotReinitialize() public {
        vm.expectRevert();
        router.initialize(maker);
    }
}
