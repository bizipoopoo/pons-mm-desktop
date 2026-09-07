// SPDX-License-Identifier: MIT
pragma solidity ^0.8.28;

interface IERC20 {
    function balanceOf(address account) external view returns (uint256);
}

/**
 * @title PonsBalanceLens
 * @notice Stateless helper: one `eth_call` returns native (and optional ERC-20)
 * balances for many accounts. The desktop app injects this bytecode via
 * `eth_call` state override so a deployment is optional; the same bytecode
 * can also be deployed and called at a fixed address.
 */
contract PonsBalanceLens {
    /// @notice Native ETH/RH balances of `accounts`, in wei, in the same order.
    function ethBalances(address[] calldata accounts) external view returns (uint256[] memory out) {
        uint256 n = accounts.length;
        out = new uint256[](n);
        for (uint256 i; i < n; ++i) {
            out[i] = accounts[i].balance;
        }
    }

    /// @notice ERC-20 `balanceOf` for each account. A reverting token call
    /// yields 0 for that account rather than failing the whole batch.
    function tokenBalances(address token, address[] calldata accounts) external view returns (uint256[] memory out) {
        uint256 n = accounts.length;
        out = new uint256[](n);
        for (uint256 i; i < n; ++i) {
            out[i] = _tokenBalance(token, accounts[i]);
        }
    }

    /// @notice Native balances plus optional ERC-20 balances in one call.
    /// Pass `token == address(0)` to skip the token reads (the token array is
    /// still returned, filled with zeros).
    function snapshot(address token, address[] calldata accounts)
        external
        view
        returns (uint256[] memory native, uint256[] memory tokens)
    {
        uint256 n = accounts.length;
        native = new uint256[](n);
        tokens = new uint256[](n);
        bool readToken = token != address(0);
        for (uint256 i; i < n; ++i) {
            native[i] = accounts[i].balance;
            if (readToken) {
                tokens[i] = _tokenBalance(token, accounts[i]);
            }
        }
    }

    function _tokenBalance(address token, address account) private view returns (uint256) {
        try IERC20(token).balanceOf(account) returns (uint256 bal) {
            return bal;
        } catch {
            return 0;
        }
    }
}
