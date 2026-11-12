# Public hashtag channel decoding

CoreScope derives an AES-128 key from each exact `hashChannels` name. Names are
case-sensitive. This supports known public hashtags; it does not discover an
unknown name or decrypt a private channel without its key.

The ingestor checks the group envelope's one-byte channel hash before trying
HMAC and AES. The hash is `SHA256(key)[0]`, matching MeshCore's 128-bit channel
format. A hash match is only a candidate: collisions still undergo the existing
MAC, ciphertext and plaintext validation. A mismatched envelope hash is not
accepted even if its ciphertext could be decrypted by a configured key.

The filter uses a stack buffer and retains no global key cache. Per-message
hash checks remain linear in the configured key count; costly HMAC/AES attempts
are limited to matching candidates (about one in 256 for a mixed catalogue).

## Measurement

A local Intel i7-7700K benchmark using 2,703 public channel names and a 128-byte
unresolved group payload measured about 4.14 ms and 1.91 MB allocated per decode
before filtering, versus 0.59 ms and 8.3 KB after filtering. This is a decoder
microbenchmark, not an end-to-end throughput claim. A 320-key comparison fell
from about 0.49 ms to 0.076 ms. Configuration should still be bounded; loading
arbitrary word lists is not a substitute for a reviewed public-channel list.

`BenchmarkGroupChannelCatalogue` provides a self-contained synthetic benchmark.
Collision, incorrect-envelope-hash, invalid-key and ordinary decode tests guard
correctness. The API server's envelope-only decoder has no key set and remains
unchanged.
