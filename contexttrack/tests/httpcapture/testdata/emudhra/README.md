# Offline eMudhra distrust fixture

Retrieved **2026-10-26** from
<https://emudhra.we-have-a-problem.com/chains.zip>, selecting only
`chains/dv-g3b-emudhra.we-have-a-problem.com.pem`, unchanged here as
`dv-g3b-chain.pem`. This is **public certificate material only**, not private
keys. Neither the archive nor any recovered keys belong in this repository.

The incident site is published by **Dennis Jackson** with community contributors;
its [source](https://github.com/galadran/we-have-a-problem.com) is licensed under
[Apache License 2.0](https://github.com/galadran/we-have-a-problem.com/blob/main/LICENSE).
See also [Go's incident report](https://go.dev/blog/emudhra-incident) and
[Go issue 82345 / CVE-2026-111943](https://go.dev/issue/82345).

SHA-256 checksums:

```text
7b949956955fc501a30f4d98d2f513d34c0bbbe92e29a968194c96e097ce2345  chains.zip
ab6483de0cb01d53d59275baf74d2766f3599e57a48d3319e4a5536c268ac696  dv-g3b-chain.pem
```

The three certificate subjects, in leaf-first order:

1. `CN=dv-g3b-emudhra.we-have-a-problem.com`
2. `C=NL, O=eMudhra BV, CN=EM DV TLS CA - G3B`
3. `C=IN, O=eMudhra Technologies Limited, CN=emSign Root TLS CA - G3`

The middle certificate's SPKI SHA-256 is
`ebed6475c2c2d80fcb199f3fe8dd6e6b3ce60b77eb15d4e42c3dec0c6f5b5105`,
one of the upstream Go 1.26.10 distrust entries.

`TestEMudhraDistrust` verifies at the fixed time **2026-10-21T12:00:00Z** with
an explicit root pool containing only certificate 3 and an intermediate pool
containing certificate 2. Certificate 3 is a cross-signed CA certificate,
intentionally treated as a **local trust anchor**: its issuer need not be trusted
by the host OS. The test does not consult or characterize the system trust store.
This setup makes the leaf otherwise valid for its DNS name and server-auth usage,
so expiry, missing roots, or hostname mismatch cannot stand in for the fix.
The unaffected-anchor control uses the same fixed time and explicit pool.

The affected-chain subtest must fail on stock Go 1.26.6 because that toolchain
accepts the chain, and pass on Go 1.26.10 with no chains and a typed
`x509.CertificateInvalidError` whose reason is `x509.NoValidChains`. The
unaffected-anchor control passes on both. No network access is used by the test.
