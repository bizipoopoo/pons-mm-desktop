// SPDX-License-Identifier: MIT
pragma solidity ^0.8.28;

import {Initializable} from "@openzeppelin/contracts-upgradeable/proxy/utils/Initializable.sol";
import {UUPSUpgradeable} from "@openzeppelin/contracts-upgradeable/proxy/utils/UUPSUpgradeable.sol";
import {OwnableUpgradeable} from "@openzeppelin/contracts-upgradeable/access/OwnableUpgradeable.sol";
import {ReentrancyGuardUpgradeable} from "@openzeppelin/contracts-upgradeable/utils/ReentrancyGuardUpgradeable.sol";

/// @dev Arbitrum Nitro precompile. `block.number` inside a contract on an
/// Orbit chain is the parent-chain height; the L2 height every RPC and
/// `newHeads` subscription reports is only available through ArbSys.
interface IArbSys {
    function arbBlockNumber() external view returns (uint256);
}

/// @dev The slice of PonsV2BondingCurve the router needs. For native-quote
/// curves `msg.value` must equal `quoteIn`; any unfilled remainder is refunded
/// to `msg.sender` (this router) and `tokensOut` is sent to `recipient`.
interface IPonsV2BondingCurve {
    function buy(uint256 quoteIn, uint256 minTokensOut, address recipient) external payable returns (uint256 tokensOut);
}

/// @dev Mirrors ILaunchpadV2.TokenParams exactly; it is ABI-encoded into the
/// official factory/router calls, so field order and types must not drift.
struct Socials {
    string twitter;
    string telegram;
    string discord;
    string website;
    string farcaster;
}

struct TokenParams {
    string name;
    string symbol;
    string logo;
    string description;
    Socials socials;
    address creatorFeeRecipient;
    uint16 creatorTaxBps;
    bool buybackEnabled;
    bytes32 expectedEconomics;
    bytes32 salt;
}

interface IPonsV2LaunchFactory {
    function launchToken(TokenParams calldata params, uint256 launchConfigId, address pairToken, address[] calldata snipeTaxExemptions)
        external
        payable
        returns (address token, address curve);
}

interface IPonsLaunchAndBuyRouter {
    function launchAndBuy(
        TokenParams calldata params,
        uint256 launchConfigId,
        address pairToken,
        uint256 quoteIn,
        uint256 minTokensOut,
        address recipient,
        address[] calldata snipeTaxExemptions
    ) external payable returns (address token, address curve, uint256 tokensOut);
}

/**
 * @title PonsMMRouter
 * @notice Launch-and-buy router for pons v2 bonding curves that lets the chain,
 * not the client, decide whether an opening buy is still "at launch".
 *
 * Three entrypoints cooperate:
 *
 *  - `launch` forwards a launch to the official pons contracts and records
 *    the L2 block it landed in. Because the launch runs through this router,
 *    the official factory records this contract as the deployer; the creator
 *    fee recipient and the opening-buy recipient are whatever the caller
 *    passes, so nothing economic moves off the caller's wallets.
 *  - `buyAfterLaunch` fills only while the current L2 block is within
 *    `maxBlocksAfterLaunch` of that recorded block. Maker buys broadcast in
 *    the same batch as the launch normally land in the same or next block;
 *    one that is delayed (rate limiting, a dropped connection) reverts
 *    instead of buying late into a curve snipers already moved.
 *  - `launchAndBuyAtomic` does the launch and every maker buy in ONE
 *    transaction, spending ETH the makers deposited here beforehand. Tokens
 *    go straight to each maker; nothing can trade between launch and the
 *    makers' fills.
 *
 * No slippage is enforced on any path: the makers are declared snipe-tax-
 * exempt first buyers and the window (or atomicity) is the only limit.
 * `buyWithin` from v1 is kept for callers that still pin an absolute height.
 */
contract PonsMMRouter is Initializable, UUPSUpgradeable, OwnableUpgradeable, ReentrancyGuardUpgradeable {
    IArbSys internal constant ARB_SYS = IArbSys(address(100));

    struct LaunchTerms {
        TokenParams params;
        uint256 launchConfigId;
        address pairToken;
        /// Opening buy executed inside the launch by the official router
        /// (0 = plain launch through the factory).
        uint256 quoteIn;
        /// Receives the opening buy's tokens; typically the treasury EOA.
        address recipient;
        /// Wallets exempted from the snipe tax. Must include every maker that
        /// will buy through this router AND the treasury, since the factory
        /// only auto-exempts the deployer (this contract) and the creator fee
        /// recipient.
        address[] snipeTaxExemptions;
    }

    struct AtomicBuy {
        address wallet; // deposit debited and tokens credited
        uint256 quoteIn;
    }

    // ---- storage (append-only across upgrades) ----
    /// L2 block each curve launched through this router landed in.
    mapping(address curve => uint256) public launchBlock;
    /// ETH makers parked here for atomic launches.
    mapping(address wallet => uint256) public deposits;
    /// Address allowed to spend a wallet's deposit inside launchAndBuyAtomic.
    mapping(address wallet => address) public operatorOf;
    /// Sum of all deposits; the rest of the balance is transient refunds.
    uint256 public totalDeposits;

    error Expired(uint256 currentL2Block, uint256 maxL2Block);
    error NotLaunchedHere(address curve);
    error ZeroAddress();
    error ZeroValue();
    error RefundFailed(address to, uint256 amount);
    error InsufficientDeposit(address wallet, uint256 have, uint256 need);
    error NotOperator(address wallet, address operator, address caller);
    error OperatorLocked(address wallet, address current);
    error LaunchValueTooLow(uint256 value, uint256 quoteIn);

    event RoutedBuy(
        address indexed caller,
        address indexed curve,
        address indexed recipient,
        uint256 quoteIn,
        uint256 tokensOut,
        uint256 refunded,
        uint256 l2Block,
        uint256 maxL2Block
    );
    event Launched(address indexed caller, address indexed token, address indexed curve, uint256 l2Block, uint256 quoteIn, uint256 tokensOut);
    event Deposited(address indexed wallet, address indexed operator, uint256 amount, uint256 balance);
    event Withdrawn(address indexed wallet, uint256 amount, uint256 balance);

    /// @custom:oz-upgrades-unsafe-allow constructor
    constructor() {
        _disableInitializers();
    }

    function initialize(address owner_) external initializer {
        if (owner_ == address(0)) revert ZeroAddress();
        __Ownable_init(owner_);
        __UUPSUpgradeable_init();
        __ReentrancyGuard_init();
    }

    /// @notice Current L2 height as the sequencer sees it.
    function currentL2Block() external view returns (uint256) {
        return ARB_SYS.arbBlockNumber();
    }

    // ------------------------------------------------------------------
    // Launch (mode A): record the block, makers buy relative to it
    // ------------------------------------------------------------------

    /**
     * @notice Launches through the official pons contracts and records the
     * L2 block. `msg.value` must cover the launch fee plus `terms.quoteIn`;
     * whatever the official contracts hand back is returned to the caller.
     */
    function launch(LaunchTerms calldata terms, address factory, address officialRouter)
        external
        payable
        nonReentrant
        returns (address token, address curve, uint256 tokensOut)
    {
        (token, curve, tokensOut) = _launch(terms, factory, officialRouter, msg.value);
        _refundFree(msg.sender);
    }

    /**
     * @notice Buys from `curve` provided the current L2 block is at most
     * `maxBlocksAfterLaunch` past the block the curve launched in through
     * this router. Reverts for curves launched elsewhere.
     */
    function buyAfterLaunch(address curve, uint256 maxBlocksAfterLaunch, address recipient)
        external
        payable
        nonReentrant
        returns (uint256 tokensOut)
    {
        uint256 lb = launchBlock[curve];
        if (lb == 0) revert NotLaunchedHere(curve);
        uint256 maxBlock = lb + maxBlocksAfterLaunch;
        uint256 current = ARB_SYS.arbBlockNumber();
        if (current > maxBlock) revert Expired(current, maxBlock);
        tokensOut = _routedBuy(curve, recipient, msg.value, current, maxBlock);
    }

    /**
     * @notice Buys from `curve` with all of `msg.value` provided the current
     * L2 block is at most `maxL2Block` (absolute height, v1 semantics).
     */
    function buyWithin(address curve, uint256 maxL2Block, address recipient)
        external
        payable
        nonReentrant
        returns (uint256 tokensOut)
    {
        uint256 current = ARB_SYS.arbBlockNumber();
        if (current > maxL2Block) revert Expired(current, maxL2Block);
        tokensOut = _routedBuy(curve, recipient, msg.value, current, maxL2Block);
    }

    // ------------------------------------------------------------------
    // Launch (mode B): one transaction, launch + every maker buy
    // ------------------------------------------------------------------

    /// @notice Parks ETH for atomic launches and names who may spend it.
    /// The operator can only be changed while the balance is zero.
    function deposit(address operator) external payable {
        if (operator == address(0)) revert ZeroAddress();
        if (msg.value == 0) revert ZeroValue();
        address current = operatorOf[msg.sender];
        if (current != address(0) && current != operator && deposits[msg.sender] != 0) {
            revert OperatorLocked(msg.sender, current);
        }
        operatorOf[msg.sender] = operator;
        deposits[msg.sender] += msg.value;
        totalDeposits += msg.value;
        emit Deposited(msg.sender, operator, msg.value, deposits[msg.sender]);
    }

    /// @notice Returns parked ETH to the depositing wallet. 0 = everything.
    function withdraw(uint256 amount) external nonReentrant {
        uint256 bal = deposits[msg.sender];
        if (amount == 0) amount = bal;
        if (amount == 0) revert ZeroValue();
        if (amount > bal) revert InsufficientDeposit(msg.sender, bal, amount);
        deposits[msg.sender] = bal - amount;
        totalDeposits -= amount;
        (bool ok,) = payable(msg.sender).call{value: amount}("");
        if (!ok) revert RefundFailed(msg.sender, amount);
        emit Withdrawn(msg.sender, amount, deposits[msg.sender]);
    }

    /**
     * @notice Launches and then fills every `buys[i]` from that wallet's
     * deposit in the same transaction. The caller must be each wallet's
     * registered operator. Unfilled quote the curve refunds is credited back
     * to the wallet's deposit; leftover `msg.value` is returned to the caller.
     */
    function launchAndBuyAtomic(LaunchTerms calldata terms, address factory, address officialRouter, AtomicBuy[] calldata buys)
        external
        payable
        nonReentrant
        returns (address token, address curve, uint256 tokensOut, uint256[] memory makerTokens)
    {
        (token, curve, tokensOut) = _launch(terms, factory, officialRouter, msg.value);
        uint256 lb = launchBlock[curve];
        makerTokens = new uint256[](buys.length);
        for (uint256 i = 0; i < buys.length; ++i) {
            AtomicBuy calldata b = buys[i];
            if (b.wallet == address(0)) revert ZeroAddress();
            if (b.quoteIn == 0) revert ZeroValue();
            if (operatorOf[b.wallet] != msg.sender) revert NotOperator(b.wallet, operatorOf[b.wallet], msg.sender);
            uint256 have = deposits[b.wallet];
            if (have < b.quoteIn) revert InsufficientDeposit(b.wallet, have, b.quoteIn);
            deposits[b.wallet] = have - b.quoteIn;
            totalDeposits -= b.quoteIn;

            uint256 balBefore = address(this).balance;
            uint256 got = IPonsV2BondingCurve(curve).buy{value: b.quoteIn}(b.quoteIn, 0, b.wallet);
            // Whatever came back beyond (balBefore - quoteIn) is the curve's
            // clamped-fill refund; it belongs to the maker, not the caller.
            uint256 refund = address(this).balance - (balBefore - b.quoteIn);
            if (refund != 0) {
                deposits[b.wallet] += refund;
                totalDeposits += refund;
            }
            makerTokens[i] = got;
            emit RoutedBuy(msg.sender, curve, b.wallet, b.quoteIn, got, refund, lb, lb);
        }
        _refundFree(msg.sender);
    }

    // ------------------------------------------------------------------
    // internals
    // ------------------------------------------------------------------

    function _launch(LaunchTerms calldata terms, address factory, address officialRouter, uint256 value)
        private
        returns (address token, address curve, uint256 tokensOut)
    {
        if (terms.recipient == address(0) || factory == address(0)) revert ZeroAddress();
        if (value < terms.quoteIn) revert LaunchValueTooLow(value, terms.quoteIn);
        if (terms.quoteIn == 0) {
            (token, curve) = IPonsV2LaunchFactory(factory).launchToken{value: value}(
                terms.params, terms.launchConfigId, terms.pairToken, terms.snipeTaxExemptions
            );
        } else {
            if (officialRouter == address(0)) revert ZeroAddress();
            (token, curve, tokensOut) = IPonsLaunchAndBuyRouter(officialRouter).launchAndBuy{value: value}(
                terms.params, terms.launchConfigId, terms.pairToken, terms.quoteIn, 0, terms.recipient, terms.snipeTaxExemptions
            );
        }
        uint256 lb = ARB_SYS.arbBlockNumber();
        launchBlock[curve] = lb;
        emit Launched(msg.sender, token, curve, lb, terms.quoteIn, tokensOut);
    }

    function _routedBuy(address curve, address recipient, uint256 quoteIn, uint256 current, uint256 maxBlock)
        private
        returns (uint256 tokensOut)
    {
        if (curve == address(0) || recipient == address(0)) revert ZeroAddress();
        if (quoteIn == 0) revert ZeroValue();
        tokensOut = IPonsV2BondingCurve(curve).buy{value: quoteIn}(quoteIn, 0, recipient);
        uint256 refund = _refundFree(msg.sender);
        emit RoutedBuy(msg.sender, curve, recipient, quoteIn, tokensOut, refund, current, maxBlock);
    }

    /// @dev Sends everything that is not a maker deposit back to `to`.
    function _refundFree(address to) private returns (uint256 refund) {
        refund = address(this).balance - totalDeposits;
        if (refund != 0) {
            (bool ok,) = payable(to).call{value: refund}("");
            if (!ok) revert RefundFailed(to, refund);
        }
    }

    /// @dev Only the curve's clamped-buy refund path and the official
    /// router's change are expected to pay in.
    receive() external payable {}

    function _authorizeUpgrade(address) internal override onlyOwner {}
}
