// SPDX-License-Identifier: MIT
pragma solidity ^0.8.28;

import {Test} from "forge-std/Test.sol";
import {ERC1967Proxy} from "@openzeppelin/contracts/proxy/ERC1967/ERC1967Proxy.sol";
import {OwnableUpgradeable} from "@openzeppelin/contracts-upgradeable/access/OwnableUpgradeable.sol";
import {PonsMMRouter, TokenParams, Socials} from "../src/PonsMMRouter.sol";

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
    mapping(address => uint256) public tokens;

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
        tokens[recipient] += spent * rate;
        return spent * rate;
    }
}

/// @dev Factory + official router stand-in. Charges an exact fee, deploys a
/// MockCurve, records deployer and exemptions, performs the opening buy.
contract MockPons {
    uint256 public constant FEE = 0.0005 ether;
    address public lastDeployer;
    address public lastCurve;
    address[] public lastExemptions;
    address public lastFeeRecipient;

    function launchToken(TokenParams calldata p, uint256, address, address[] calldata ex)
        external
        payable
        returns (address token, address curve)
    {
        require(msg.value == FEE, "fee");
        return _launch(p, ex);
    }

    function launchAndBuy(TokenParams calldata p, uint256, address, uint256 quoteIn, uint256 minOut, address recipient, address[] calldata ex)
        external
        payable
        returns (address token, address curve, uint256 tokensOut)
    {
        require(msg.value == FEE + quoteIn, "fee+quote");
        (token, curve) = _launch(p, ex);
        tokensOut = MockCurve(curve).buy{value: quoteIn}(quoteIn, minOut, recipient);
    }

    function _launch(TokenParams calldata p, address[] calldata ex) private returns (address token, address curve) {
        lastDeployer = msg.sender;
        lastFeeRecipient = p.creatorFeeRecipient;
        delete lastExemptions;
        for (uint256 i = 0; i < ex.length; ++i) {
            lastExemptions.push(ex[i]);
        }
        curve = address(new MockCurve());
        lastCurve = curve;
        token = address(uint160(uint256(keccak256(abi.encode(curve)))));
    }
}

contract PonsMMRouterV3Mock is PonsMMRouter {
    function version() external pure returns (uint256) {
        return 3;
    }
}

contract PonsMMRouterTest is Test {
    MockArbSys arbSys;
    MockCurve curve;
    MockPons pons;
    PonsMMRouter router;
    address owner = makeAddr("owner");
    address treasury = makeAddr("treasury");
    address maker = makeAddr("maker");
    address maker2 = makeAddr("maker2");

    function setUp() public {
        MockArbSys impl = new MockArbSys();
        vm.etch(address(100), address(impl).code);
        arbSys = MockArbSys(address(100));
        arbSys.set(1_000_000);

        curve = new MockCurve();
        pons = new MockPons();
        PonsMMRouter logic = new PonsMMRouter();
        bytes memory init = abi.encodeCall(PonsMMRouter.initialize, (owner));
        router = PonsMMRouter(payable(address(new ERC1967Proxy(address(logic), init))));
        vm.deal(treasury, 10 ether);
        vm.deal(maker, 10 ether);
        vm.deal(maker2, 10 ether);
    }

    function terms(uint256 quoteIn) internal view returns (PonsMMRouter.LaunchTerms memory t) {
        address[] memory ex = new address[](2);
        ex[0] = treasury;
        ex[1] = maker;
        t = PonsMMRouter.LaunchTerms({
            params: TokenParams({
                name: "T",
                symbol: "T",
                logo: "",
                description: "",
                socials: Socials("", "", "", "", ""),
                creatorFeeRecipient: treasury,
                creatorTaxBps: 0,
                buybackEnabled: true,
                expectedEconomics: bytes32(0),
                salt: bytes32(uint256(1))
            }),
            launchConfigId: 0,
            pairToken: address(0),
            quoteIn: quoteIn,
            recipient: treasury,
            snipeTaxExemptions: ex
        });
    }

    // ---- v1 buyWithin -------------------------------------------------

    function test_buyWithinInsideWindow() public {
        vm.prank(maker);
        uint256 out = router.buyWithin{value: 1 ether}(address(curve), 1_000_002, maker);
        assertEq(out, 1 ether * 1000);
        assertEq(curve.lastRecipient(), maker);
        assertEq(curve.lastMinOut(), 0, "no slippage bound");
        assertEq(curve.lastSender(), address(router));
        assertEq(address(router).balance, 0);
    }

    function test_buyWithinRevertsPastWindow() public {
        arbSys.set(1_000_004);
        vm.prank(maker);
        vm.expectRevert(abi.encodeWithSelector(PonsMMRouter.Expired.selector, 1_000_004, 1_000_003));
        router.buyWithin{value: 1 ether}(address(curve), 1_000_003, maker);
    }

    function test_buyWithinRefundForwarded() public {
        curve.setCap(0.25 ether);
        uint256 before = maker.balance;
        vm.prank(maker);
        router.buyWithin{value: 1 ether}(address(curve), 1_000_001, maker);
        assertEq(before - maker.balance, 0.25 ether);
        assertEq(address(router).balance, 0);
    }

    // ---- mode A: launch records block, buys relative to it ------------

    function test_launchRecordsBlockAndRefundsExtra() public {
        arbSys.set(2_000_000);
        uint256 before = treasury.balance;
        uint256 fee = pons.FEE();
        PonsMMRouter.LaunchTerms memory t = terms(0.1 ether);
        vm.prank(treasury);
        (, address c, uint256 out) = router.launch{value: fee + 0.1 ether}(t, address(pons), address(pons));
        assertEq(c, pons.lastCurve());
        assertEq(router.launchBlock(c), 2_000_000);
        assertEq(out, 0.1 ether * 1000, "opening buy through the official router");
        assertEq(MockCurve(c).lastRecipient(), treasury, "opening buy tokens go to the recipient");
        assertEq(pons.lastDeployer(), address(router), "official contracts see the router as deployer");
        assertEq(pons.lastFeeRecipient(), treasury);
        assertEq(before - treasury.balance, fee + 0.1 ether);
        assertEq(address(router).balance, 0);
    }

    function test_launchWithoutOpeningBuyUsesFactory() public {
        uint256 fee = pons.FEE();
        PonsMMRouter.LaunchTerms memory t = terms(0);
        vm.prank(treasury);
        (, address c, uint256 out) = router.launch{value: fee}(t, address(pons), address(0));
        assertEq(out, 0);
        assertEq(router.launchBlock(c), 1_000_000);
    }

    function test_buyAfterLaunchWithinWindow() public {
        uint256 fee = pons.FEE();
        PonsMMRouter.LaunchTerms memory t = terms(0);
        vm.prank(treasury);
        (, address c,) = router.launch{value: fee}(t, address(pons), address(0));
        arbSys.set(1_000_003);
        vm.prank(maker);
        uint256 out = router.buyAfterLaunch{value: 1 ether}(c, 3, maker);
        assertEq(out, 1 ether * 1000);
        assertEq(MockCurve(c).lastRecipient(), maker);
    }

    function test_buyAfterLaunchExpired() public {
        uint256 fee = pons.FEE();
        PonsMMRouter.LaunchTerms memory t = terms(0);
        vm.prank(treasury);
        (, address c,) = router.launch{value: fee}(t, address(pons), address(0));
        arbSys.set(1_000_004);
        vm.prank(maker);
        vm.expectRevert(abi.encodeWithSelector(PonsMMRouter.Expired.selector, 1_000_004, 1_000_003));
        router.buyAfterLaunch{value: 1 ether}(c, 3, maker);
    }

    function test_buyAfterLaunchUnknownCurve() public {
        vm.prank(maker);
        vm.expectRevert(abi.encodeWithSelector(PonsMMRouter.NotLaunchedHere.selector, address(curve)));
        router.buyAfterLaunch{value: 1 ether}(address(curve), 3, maker);
    }

    // ---- mode B: deposits + atomic launch -----------------------------

    function test_depositWithdrawAndOperatorLock() public {
        vm.prank(maker);
        router.deposit{value: 1 ether}(treasury);
        assertEq(router.deposits(maker), 1 ether);
        assertEq(router.operatorOf(maker), treasury);
        assertEq(router.totalDeposits(), 1 ether);

        vm.prank(maker);
        vm.expectRevert(abi.encodeWithSelector(PonsMMRouter.OperatorLocked.selector, maker, treasury));
        router.deposit{value: 1 ether}(maker2);

        vm.prank(maker);
        router.withdraw(0.4 ether);
        assertEq(router.deposits(maker), 0.6 ether);
        vm.prank(maker);
        router.withdraw(0);
        assertEq(router.deposits(maker), 0);
        assertEq(router.totalDeposits(), 0);
        assertEq(maker.balance, 10 ether);

        // Balance is zero again: a new operator may be named.
        vm.prank(maker);
        router.deposit{value: 1 ether}(maker2);
        assertEq(router.operatorOf(maker), maker2);
    }

    function test_launchAndBuyAtomic() public {
        vm.prank(maker);
        router.deposit{value: 1 ether}(treasury);
        vm.prank(maker2);
        router.deposit{value: 2 ether}(treasury);

        PonsMMRouter.AtomicBuy[] memory buys = new PonsMMRouter.AtomicBuy[](2);
        buys[0] = PonsMMRouter.AtomicBuy(maker, 1 ether);
        buys[1] = PonsMMRouter.AtomicBuy(maker2, 1.5 ether);

        uint256 before = treasury.balance;
        uint256 fee = pons.FEE();
        PonsMMRouter.LaunchTerms memory t = terms(0.1 ether);
        vm.prank(treasury);
        (, address c, uint256 out, uint256[] memory makerTokens) =
            router.launchAndBuyAtomic{value: fee + 0.1 ether}(t, address(pons), address(pons), buys);
        assertEq(out, 0.1 ether * 1000);
        assertEq(makerTokens[0], 1 ether * 1000);
        assertEq(makerTokens[1], 1.5 ether * 1000);
        assertEq(MockCurve(c).tokens(maker), 1 ether * 1000, "tokens credited to maker");
        assertEq(MockCurve(c).tokens(maker2), 1.5 ether * 1000);
        assertEq(router.deposits(maker), 0);
        assertEq(router.deposits(maker2), 0.5 ether, "unspent deposit stays");
        assertEq(router.totalDeposits(), 0.5 ether);
        assertEq(address(router).balance, 0.5 ether, "router holds exactly the deposits");
        assertEq(before - treasury.balance, fee + 0.1 ether);
        assertEq(router.launchBlock(c), 1_000_000);
    }

    function test_atomicRefundCreditsMakerDeposit() public {
        vm.prank(maker);
        router.deposit{value: 1 ether}(treasury);
        PonsMMRouter.AtomicBuy[] memory buys = new PonsMMRouter.AtomicBuy[](1);
        buys[0] = PonsMMRouter.AtomicBuy(maker, 1 ether);
        // Launch first so we can cap the new curve; the cap applies to the
        // maker buy inside the same tx via a second launch on a fresh curve.
        uint256 fee = pons.FEE();
        PonsMMRouter.LaunchTerms memory t = terms(0);
        vm.prank(treasury);
        (, address c,,) = router.launchAndBuyAtomic{value: fee}(t, address(pons), address(0), buys);
        assertEq(MockCurve(c).tokens(maker), 1 ether * 1000);
        assertEq(router.deposits(maker), 0);
    }

    function test_atomicRequiresOperator() public {
        vm.prank(maker);
        router.deposit{value: 1 ether}(maker2); // operator is maker2, not treasury
        PonsMMRouter.AtomicBuy[] memory buys = new PonsMMRouter.AtomicBuy[](1);
        buys[0] = PonsMMRouter.AtomicBuy(maker, 1 ether);
        uint256 fee = pons.FEE();
        PonsMMRouter.LaunchTerms memory t = terms(0);
        vm.expectRevert(abi.encodeWithSelector(PonsMMRouter.NotOperator.selector, maker, maker2, treasury));
        vm.prank(treasury);
        router.launchAndBuyAtomic{value: fee}(t, address(pons), address(0), buys);
    }

    function test_atomicRequiresDeposit() public {
        vm.prank(maker);
        router.deposit{value: 0.5 ether}(treasury);
        PonsMMRouter.AtomicBuy[] memory buys = new PonsMMRouter.AtomicBuy[](1);
        buys[0] = PonsMMRouter.AtomicBuy(maker, 1 ether);
        uint256 fee = pons.FEE();
        PonsMMRouter.LaunchTerms memory t = terms(0);
        vm.expectRevert(abi.encodeWithSelector(PonsMMRouter.InsufficientDeposit.selector, maker, 0.5 ether, 1 ether));
        vm.prank(treasury);
        router.launchAndBuyAtomic{value: fee}(t, address(pons), address(0), buys);
    }

    function test_depositsSurviveRoutedBuyRefund() public {
        // A stranger's routed buy must never leak parked deposits.
        vm.prank(maker);
        router.deposit{value: 1 ether}(treasury);
        curve.setCap(0.25 ether);
        uint256 before = maker2.balance;
        vm.prank(maker2);
        router.buyWithin{value: 1 ether}(address(curve), 1_000_001, maker2);
        assertEq(before - maker2.balance, 0.25 ether, "only the clamped refund came back");
        assertEq(address(router).balance, 1 ether, "deposit untouched");
    }

    // ---- admin --------------------------------------------------------

    function test_upgradeOnlyOwner() public {
        PonsMMRouterV3Mock v3 = new PonsMMRouterV3Mock();
        vm.prank(maker);
        vm.expectRevert(abi.encodeWithSelector(OwnableUpgradeable.OwnableUnauthorizedAccount.selector, maker));
        router.upgradeToAndCall(address(v3), "");

        vm.prank(maker);
        router.deposit{value: 1 ether}(treasury);
        vm.prank(owner);
        router.upgradeToAndCall(address(v3), "");
        assertEq(PonsMMRouterV3Mock(payable(address(router))).version(), 3);
        assertEq(router.owner(), owner, "state survives upgrade");
        assertEq(router.deposits(maker), 1 ether, "deposits survive upgrade");
    }

    function test_cannotReinitialize() public {
        vm.expectRevert();
        router.initialize(maker);
    }
}
