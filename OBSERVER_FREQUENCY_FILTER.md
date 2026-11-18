# Observer reception frequency policy

`observerFrequencyRules` is an optional configuration map keyed by observer MQTT
IATA region. Example:

```json
{"observerFrequencyRules":{"SMF":{"mhz":909.875,"toleranceKHz":0.5,"maxStatusAgeSec":1800,"dropUnknown":false}}}
```

Restart the ingestor after saving a changed policy. The admin Customization →
Frequency panel edits this policy; a successful save is not an activation claim.
An absent rule disables filtering for that region.

The filter runs before packet decoding, observation writes and node updates. It
uses explicit reception `radio` metadata first, otherwise fresh observer `/status`
radio metadata, keyed by MQTT source, region and observer feed. For multi-radio
observers `rx_radio_id` selects that physical radio's status; missing selected-radio
metadata is unknown, never replaced by another radio's frequency. The first tuple
component is MHz; explicit Hz/kHz/MHz and unlabelled frequency units are normalized.
0.5 kHz default tolerance accommodates serialization rounding, not channel width.

Statuses remain accepted so a retuned observer recovers without being manually
unblocked. Unknown, expired or invalid frequency evidence is accepted unless
`dropUnknown` is enabled. Strict mode can discard otherwise valid receptions during
startup or missing status reports. Status has a 30-minute default freshness limit;
undated retained messages are not fresh evidence. Dated snapshots outside that
window (or >5 minutes in the future) are unknown. No packet contents can prove its
RF reception frequency when the uploader does not provide reliable metadata.

Historical records are not removed. Blacklists still take precedence. The cache
is bounded to 8,192 observer feeds with at most 64 radio entries each; reception
lookup is O(1), no per-packet SQL or network calls. Drop logs are throttled to one
per configured region/minute. Misreported status and unannounced retunes remain
limitations; this is metadata-based rejection, not RF measurement.
