# Security Policy

## Reporting

Do not open a public issue for vulnerabilities that could expose private keys, signing operations, wallet balances, or transaction integrity.

Report security concerns through GitHub's private vulnerability reporting feature for this repository. Include affected versions, reproduction steps, impact, and a minimal proof of concept without real credentials.

## Supported Versions

Only the latest published release is supported with security fixes.

## Secret Handling

PonsDesk never needs a private key in source code, environment variables, GitHub Actions secrets, or command-line flags. Wallet material belongs only in the local encrypted vault. Test reports and screenshots must use generated, unfunded accounts.
