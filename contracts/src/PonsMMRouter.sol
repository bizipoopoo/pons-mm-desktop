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

/**
 * @title PonsMMRouter
 * @notice Block-height-limited buy router for pons v2 bonding curves.
 *
 * A launch bundle pre-signs one buy per maker wallet against the CREATE2
 * predicted curve address and broadcasts them alongside the launch itself.
 * Those buys must land within a few blocks of submission or not at all: a buy
 * that fills seconds late, after snipers have already moved the curve, is
 * worse than no buy. Slippage cannot express that, because the makers are
 * snipe-tax-exempt first buyers who want any price inside the window, so the
 * window is enforced here instead of the curve's `minTokensOut`.
 *
 * The router holds no funds between calls and has no privileged path other
 * than upgrades, which are owner-only (UUPS).
 */
contract PonsMMRouter is Initializable, UUPSUpgradeable, OwnableUpgradeable, ReentrancyGuardUpgradeable {
    IArbSys internal constant ARB_SYS = IArbSys(address(100));

    error Expired(uint256 currentL2Block, uint256 maxL2Block);
    error ZeroAddress();
    error ZeroValue();
    error RefundFailed(address to, uint256 amount);

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

    /// @notice Current L2 height as the sequencer sees it, for callers that
    /// want to pin `maxL2Block` from the same clock the check uses.
    function currentL2Block() external view returns (uint256) {
        return ARB_SYS.arbBlockNumber();
    }

    /**
     * @notice Buys from `curve` with all of `msg.value`, crediting `recipient`,
     * provided the current L2 block is at most `maxL2Block`.
     * @dev No slippage bound is applied (`minTokensOut = 0`); the block window
     * is the only limit. Any quote the curve refunds because the buy was
     * clamped at the sellable supply is forwarded back to the caller.
     */
    function buyWithin(address curve, uint256 maxL2Block, address recipient)
        external
        payable
        nonReentrant
        returns (uint256 tokensOut)
    {
        uint256 current = ARB_SYS.arbBlockNumber();
        if (current > maxL2Block) revert Expired(current, maxL2Block);
        if (curve == address(0) || recipient == address(0)) revert ZeroAddress();
        if (msg.value == 0) revert ZeroValue();

        tokensOut = IPonsV2BondingCurve(curve).buy{value: msg.value}(msg.value, 0, recipient);

        // The curve refunds clamped quote to msg.sender, which is this router.
        uint256 refund = address(this).balance;
        if (refund != 0) {
            (bool ok,) = payable(msg.sender).call{value: refund}("");
            if (!ok) revert RefundFailed(msg.sender, refund);
        }
        emit RoutedBuy(msg.sender, curve, recipient, msg.value, tokensOut, refund, current, maxL2Block);
    }

    /// @dev Only the curve's clamped-buy refund path is expected to pay in.
    receive() external payable {}

    function _authorizeUpgrade(address) internal override onlyOwner {}
}
