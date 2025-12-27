# WoT bucket-based reputation rate limiting (wotrlay v2) — practical analysis

This document analyzes the **web-of-trust (WoT) + true token bucket rate limit model** implemented in the wotrlay v2 example, focusing on how it behaves in practice: mechanics, expected effectiveness, scenario behavior, benefits, and limitations.

Primary implementation reference: [`main.go`](main.go:1), [`rate.go`](rate.go:1), [`rank.go`](rank.go:1).

---

## 1) What the model is trying to achieve

The design goal is to protect a relay's finite resources (storage, bandwidth, CPU, moderation cost) while still allowing **high-trust/high-reputation** identities to publish at higher volume.

It combines:

1. **Per-pubkey rate limiting** (who is writing?)
2. **Reputation-weighted allowances** (how trusted is that pubkey?)
3. **Kind-based gating** (what types of content are allowed?)
4. **IP-based throttling for rank lookups** (who is trying to force expensive rank computations?)

This is a **true token bucket approach** with:

- **Continuous refill** (not daily reset) for smooth rate limiting
- **Burst capacity** (1 hour worth of tokens) for natural usage patterns
- **Trust-tiered publishing policy** with kind restrictions
- **Minimal IP limiter** only for rank-queue protection

The reputation source is an external service (ContextVM/Relatr) returning a trust score per pubkey.

---

## 2) Exact mechanics in the current code

### 2.1 No NIP-42 authentication requirement

Unlike the previous version, v2 does **not** require NIP-42 authentication:

- Rate limiting is enforced per event using `event.PubKey`: [`handleEvent()`](main.go:162)
- No connection-level auth checks
- No `rely.WithoutMultiAuth()` restriction

**Practical effect:** any client can publish events, but rate limiting is enforced based on the event's signed pubkey. This improves UX and compatibility with typical Nostr clients.

### 2.2 True token bucket with continuous refill

The token bucket is implemented in [`rate.go`](rate.go:1) with classic semantics:

**Bucket fields** ([`Bucket`](rate.go:19)):
- `tokens` (float64): current token count
- `capacity` (float64): maximum tokens held
- `refillRate` (float64): tokens per second
- `lastActive` (time.Time): last refill or consume time

**Refill algorithm** ([`refillLocked()`](rate.go:117)):
```
elapsed = now - lastActive
tokens = min(capacity, tokens + elapsed * refillRate)
lastActive = now
```

**Consumption** ([`Consume()`](rate.go:75)):
1. Refill based on elapsed time
2. Check if `tokens >= cost`
3. If yes: `tokens -= cost` and return true
4. If no: return false

**Practical effect:** tokens accumulate continuously at a steady rate, allowing smooth publication patterns with bounded bursts.

### 2.3 Trust-tiered publishing policy

The trust score `r ∈ [0,1]` determines:

1. **Which kinds are allowed** (kind gating)
2. **The token bucket refill rate** (events per second)
3. **The bucket capacity** (burst allowance)

**Kind gating** ([`handleEvent()`](main.go:172)):
- If `r < midThreshold` and `e.Kind != 1`: reject with [`ErrKindNotAllowed`](main.go:68)
- Otherwise: all kinds allowed

**Daily allowance calculation** ([`calculateDailyRate()`](main.go:206)):

**Tier A: r == 0**
- Allowed kinds: Kind 1 only
- `daily(r) = 1`

**Tier B: 0 < r < midThreshold**
- Allowed kinds: Kind 1 only
- Linear from 1/day to 100/day
- `daily(r) = 1 + (r/midThreshold) * (100 - 1)`

**Tier C: midThreshold ≤ r < highThreshold**
- Allowed kinds: all
- Linear from 100/day to 5000/day
- `daily(r) = 100 + ((r - midThreshold)/(highThreshold - midThreshold)) * (5000 - 100)`

**Tier D: r ≥ highThreshold**
- Allowed kinds: all
- Cap at 10,000/day
- `daily(r) = 10000`

**Token bucket parameters** ([`handleEvent()`](main.go:189-196)):
- `refillRate = daily(r) / 86400` (tokens per second)
- `capacity = daily(r) / 24.0` (1 hour worth of tokens)
- **Important modification**: if `capacity < 1`, it's set to `1` to prevent permanent rate limiting for low-rate pubkeys

### 2.4 Backfill rule for very high trust

High-trust pubkeys can backfill old events without consuming tokens ([`handleEvent()`](main.go:182-186)):

- If `r ≥ highThreshold` and `now - e.CreatedAt > 24h`: skip rate limiting
- Otherwise: normal token bucket consumption

**Timestamp sanity** ([`handleEvent()`](main.go:177-180)):
- Reject events where `e.CreatedAt - now > 24h` (future events)

### 2.5 IP-level rate limiting (rank-queue protection only)

IP throttling is **only** applied when a rank lookup is needed ([`lookupRank()`](main.go:227-250)):

- Key: `rankQueueKeyPrefix + ipGroup` (e.g., `"rank-queue:192.168.1.0"`)
- Default limit: 100 rank refresh requests per day per IP group
- Refill rate: `100 / 86400` tokens per second (continuous)
- Capacity: 100 tokens (1 day worth)

**Behavior:**
- On cache miss, check IP limiter
- If allowed: attempt immediate refresh with 2s timeout
- If refresh succeeds: use returned rank
- If refresh fails or times out: enqueue for async refresh, proceed with `rank=0`
- If IP limiter denies: skip refresh, proceed with `rank=0`

**Key difference from v1:** No connection-level IP blocking. Only rank-queue operations are throttled.

### 2.6 Rank cache and refresh pipeline

Ranks are stored in an in-memory cache: [`RankCache`](rank.go:22).

**Cache structure** ([`TimeRank`](rank.go:46)):
- `Rank` (float64): trust score
- `Timestamp` (time.Time): when this rank was cached

**Cache lookup** ([`Rank()`](rank.go:149)):
- Returns `(rank, exists)` tuple
- If not exists: enqueue for refresh, return `(0, false)`
- If stale (>24h): enqueue for refresh, return cached rank

**Immediate lookup** ([`GetRank()`](rank.go:178)):
- Blocks until rank is available
- Uses singleflight to prevent duplicate network requests
- On failure: caches `rank=0` to avoid repeated lookups

**Refresh batching** ([`refresher()`](rank.go:271)):
- Batch size: up to 1000 pubkeys ([`MaxPubkeysToRank`](rank.go:265))
- Flush triggers:
  - Batch reaches 1000 unique pubkeys
  - Periodic flush every 24h ([`StaleThreshold`](rank.go:100))
- Deduplication: uses `seen` map to avoid duplicates in batch

**External dependency:** rank resolution calls ContextVM's `calculate_trust_scores` via JSON-RPC over a relay endpoint: [`refreshBatch()`](rank.go:324).

**Cache cleanup** ([`updateAndClean()`](rank.go:241)):
- Eviction threshold: 7 days ([`MaxRefreshInterval`](rank.go:101))
- Cleanup runs every 3.5 days (half of MaxRefreshInterval)
- Only runs if enough time has elapsed since last clean

---

## 3) How this behaves in practice

### 3.1 True token bucket: smooth rate limiting with bounded bursts

Unlike v1's "daily reset" behavior, v2 uses continuous refill:

**Consequences:**
- Users cannot burst their entire daily quota instantly
- Publication rate is smoothed over time
- Burst capacity is limited to 1 hour worth of tokens
- Load on the relay is more predictable

**Example:** A pubkey with `r=0.8` (Tier C) gets ~3775 events/day:
- Refill rate: ~0.0437 tokens/second (1 event every ~23 seconds)
- Capacity: ~157 tokens (1 hour worth)
- Can publish up to 157 events in a burst, then must wait for refill

### 3.2 Unknown pubkey cold start (improved from v1)

Unknown pubkeys are treated as `r=0`:

- Allowed: Kind 1 only
- Daily quota: 1 event
- Refill rate: ~0.0000116 tokens/second
- Capacity: 1 token (due to minimum capacity rule)

**Practical effect:**
- Newcomers can publish 1 Kind 1 event immediately
- After publishing, they must wait ~24 hours for another token
- Rank refresh is attempted (gated by IP limiter)
- Once ranked with non-zero score, quota increases

**Improvement over v1:** Unknown pubkeys can actually publish (1 event/day) instead of being completely blocked.

### 3.3 Low-trust pubkeys: very constrained but not blocked

For `0 < r < midThreshold` (Tier B):

- Allowed: Kind 1 only
- Daily quota: 1 to 100 events (linear)
- Capacity: 1 token (due to minimum capacity rule)

**Example:** `r=0.2` with `midThreshold=0.5`:
- Daily: ~40.6 events
- Refill rate: ~0.00047 tokens/second
- Capacity: 1 token (clamped)

**Practical effect:**
- Low-trust users can publish, but very slowly
- The minimum capacity of 1 token means they can publish 1 event, then wait for refill
- For `r=0.2`, wait time between events is ~35 minutes
- This creates a "trickle" rather than spam

### 3.4 Threshold pubkeys: basic participation

For `r = midThreshold` (Tier C boundary):

- Allowed: all kinds
- Daily: 100 events
- Refill rate: ~0.00116 tokens/second
- Capacity: ~4.17 tokens

**Practical effect:**
- Users at the threshold can participate fully
- Can publish ~4 events in a burst, then ~1 event every ~14 minutes
- This tier is intentionally not bursty

### 3.5 High-trust pubkeys: active publishing

For `midThreshold < r < highThreshold` (Tier C):

- Allowed: all kinds
- Daily: 100 to 5000 events (linear)
- Capacity: 4.17 to ~208 tokens

**Example:** `r=0.8` with `midThreshold=0.5`, `highThreshold=0.9`:
- Daily: ~3775 events
- Refill rate: ~0.0437 tokens/second
- Capacity: ~157 tokens

**Practical effect:**
- Trusted profiles can publish actively
- Burst is controlled: ~157 events max, then ~1 event every ~23 seconds
- Suitable for power users and bots

### 3.6 Very high-trust pubkeys: maximum rate + backfill

For `r ≥ highThreshold` (Tier D):

- Allowed: all kinds
- Daily: 10,000 events
- Refill rate: ~0.116 tokens/second
- Capacity: ~417 tokens
- **Backfill:** old events (>24h) are free

**Practical effect:**
- Heavy publishers are supported
- Burst capacity: ~417 events (1 hour worth)
- Can backfill historical content without rate limiting
- Suitable for migration scenarios

### 3.7 IP limiter: minimal collateral damage

The IP limiter only affects rank-queue operations:

- Default: 100 rank refresh requests per day per IP group
- Does not block connections
- Does not block publishing for already-ranked keys

**Practical effect:**
- Shared IP users (NAT, VPN, Tor) can still publish normally
- Only rank lookups are throttled
- Attackers cannot force unlimited rank refresh work
- Much less NAT pain than v1's connection-level IP throttling

### 3.8 Rank cache staleness and refresh timing

Ranks become "stale" after 24h and are enqueued for refresh.

Refresh triggers:
- Batch reaches 1000 unique pubkeys
- Periodic flush every 24h

**Consequences:**
- In small relays with low write volume, ranks may stay stale for up to 24h
- Stale ranks aren't harmful for coarse ordering
- The cache is purely in-memory; process restarts clear it
- Singleflight prevents duplicate network requests for the same pubkey

### 3.9 Bucket TTL and cleanup

Limiter bucket eviction is controlled by:

- [`Limiter.TimeToLive`](rate.go:15): 1 hour
- [`Limiter.CleanupInterval`](rate.go:16): 24 hours

**Practical effect:**
- Buckets are eligible for deletion after 1 hour of inactivity
- Cleanup runs every 24 hours
- Deleted buckets are recreated with full capacity on next use
- This is acceptable because refill is continuous, not based on lastRequest

---

## 4) Effectiveness: what attacks it helps with

### 4.1 Sybil resistance (improved from v1)

Sybil spam is "create many identities, each low trust". This system makes that expensive:

- Unknown pubkeys get 1 event/day (Kind 1 only)
- Low-trust pubkeys get very limited rates
- Rank lookups are throttled by IP

**Improvement over v1:** Unknown pubkeys can publish 1 event, allowing minimal participation while still preventing spam.

### 4.2 Protects backend ranking budget

The IP bucket gate in [`lookupRank()`](main.go:227) prevents an attacker from forcing the relay to continuously enqueue huge numbers of pubkeys for external ranking.

### 4.3 Self-tuning allocation aligned with "social trust"

If ranks are meaningful and hard to game, this is a nice "market" mechanism:

- Trusted identities can write more
- Untrusted identities are constrained
- Kind gating adds another layer of protection

### 4.4 Smooth load shaping

The true token bucket provides:

- Predictable load on the relay
- No bursty "daily reset" spikes
- Bounded bursts (1 hour worth)

This is a significant improvement over v1 for operational stability.

---

## 5) Limitations and failure modes

### 5.1 The rate limit is only as good as the rank oracle

This design delegates a major security decision to an external rank provider (ContextVM/Relatr).

Risks:

- If the provider is down/slow, onboarding and quota enforcement can degrade
- If the ranking algorithm is gameable, attackers may inflate rank and obtain high quotas
- If rank distribution is extremely skewed, a few keys may receive huge quotas

Operationally, this adds:

- Dependency management
- Monitoring
- Fallback behavior

### 5.2 Cold-start still has friction

Unknown pubkeys get 1 event/day (Kind 1 only). This is better than v1's complete block, but still:

- Legitimate new users have very limited initial capacity
- New communities may struggle to bootstrap
- Key rotations are constrained
- Privacy-conscious users who don't want their identity graph evaluated are limited

### 5.3 Low-trust tiers have very slow publication

For `0 < r < midThreshold`, the minimum capacity of 1 token means:

- Users can publish 1 event, then wait for refill
- For `r=0.2`, wait time is ~35 minutes between events
- This may feel restrictive for legitimate low-trust users

### 5.4 No global capacity enforcement beyond per-key math

The code calculates per-key quotas based on rank, but there's no global cap.

If:

- Ranks aren't normalized the way you think
- You cache only a subset
- The provider's rank scale changes

Then the sum of per-key quotas can exceed your true capacity.

### 5.5 In-memory buckets + cache: restart resets state

Because both limiter buckets and rank cache are in-memory:

- A restart wipes state
- Active keys may temporarily lose quota state
- Ranking requests may surge after restart

However, because refill is continuous, this is less problematic than v1's daily reset model.

### 5.6 Backfill could be abused

The backfill rule (`r ≥ highThreshold` and event >24h old) allows free publishing of old events.

Potential abuse:

- An attacker with a high-trust key could backfill spam
- Old spam could be injected without rate limiting

Mitigation: Content moderation and kind filtering should still apply.

---

## 6) Scenario walkthroughs

### Scenario A: Brand new pubkey (unknown rank)

1. Client publishes event with new pubkey
2. Cache miss ⇒ treat `r=0`
3. Kind check: if Kind 1, proceed; otherwise reject
4. Token bucket: capacity=1, refill=~1/day
5. If tokens available: consume and save
6. Rank refresh attempted (gated by IP limiter)
7. After rank fetch succeeds, subsequent events use new rank

**Behavior:** Newcomers can post 1 Kind 1 note immediately, then wait ~24h for another. Onboarding friction is reduced compared to v1.

### Scenario B: Low trust (r=0.2, midThreshold=0.5)

1. Kind check: Kind 1 only
2. Daily ≈ 40.6 events
3. Capacity: 1 token (clamped)
4. Refill rate: ~0.00047 tokens/second

**Behavior:** Can publish 1 event, then wait ~35 minutes for another. Very constrained but not blocked.

### Scenario C: Threshold trust (r=0.5, midThreshold=0.5)

1. Kind check: all kinds allowed
2. Daily = 100 events
3. Capacity: ~4.17 tokens
4. Refill rate: ~0.00116 tokens/second

**Behavior:** Can publish ~4 events in a burst, then ~1 event every ~14 minutes. Basic relay participation.

### Scenario D: High trust (r=0.8, midThreshold=0.5, highThreshold=0.9)

1. Kind check: all kinds allowed
2. Daily ≈ 3775 events
3. Capacity: ~157 tokens
4. Refill rate: ~0.0437 tokens/second

**Behavior:** Can publish ~157 events in a burst, then ~1 event every ~23 seconds. Active publishing.

### Scenario E: Very high trust (r=0.95, highThreshold=0.9)

1. Kind check: all kinds allowed
2. Daily = 10,000 events
3. Capacity: ~417 tokens
4. Refill rate: ~0.116 tokens/second
5. Backfill: old events (>24h) are free

**Behavior:** Can publish ~417 events in a burst, then ~1 event every ~8.6 seconds. Can backfill old content freely.

### Scenario F: Attacker cycles many pubkeys from one IP

1. Each pubkey is effectively capped at 1/day initially (Kind 1 only)
2. Ranking abuse: each cache miss tries to enqueue
3. Rank-queue IP limiter: 100 refresh requests per day per IP

**Behavior:** Attackers can't force large ranking workloads. Attackers can't spam the relay with new keys (publish allowance is minimal).

### Scenario G: Shared IP (NAT)

1. Ordinary publishing is per-pubkey; shared IP doesn't reduce publishing directly
2. Only rank-queue operations are IP-limited

**Behavior:** Much less NAT pain than v1's connection-level IP throttling. Legitimate users behind shared IPs can publish normally.

---

## 7) Benefits summary

- **Smooth rate limiting:** True token bucket with continuous refill, not daily reset
- **Bounded bursts:** 1 hour worth of tokens prevents load spikes
- **Improved onboarding:** Unknown pubkeys can publish 1 event/day (Kind 1 only)
- **Minimal IP throttling:** Only rank-queue operations are IP-limited, reducing NAT collateral damage
- **Kind gating:** Low-trust users can only publish Kind 1, reducing spam surface
- **Backfill support:** High-trust users can migrate old content without rate limiting
- **Self-tuning allocation:** Trusted identities get higher quotas automatically
- **Predictable load:** Smooth refill makes relay load more predictable

---

## 8) Limitations summary

- **Depends on external rank oracle:** Correctness and availability of ContextVM/Relatr
- **Cold-start friction:** New users still have limited initial capacity
- **Low-trust very slow:** Users with low trust can only publish very slowly
- **No global capacity cap:** Per-key quotas could sum to more than relay capacity
- **In-memory state:** Restart resets buckets and cache
- **Backfill abuse potential:** High-trust keys could backfill spam

---

## 9) Key behavioral differences vs v1

| Aspect | v1 (daily reset) | v2 (true token bucket) |
|--------|------------------|------------------------|
| Refill mechanism | Reset to full after 24h | Continuous refill based on elapsed time |
| Burst behavior | Can dump entire daily quota instantly | Bounded to 1 hour worth of tokens |
| Unknown pubkey | Cannot publish (0 tokens) | Can publish 1 Kind 1 event/day |
| IP throttling | Connection-level + rank-queue | Rank-queue only |
| NIP-42 auth | Required | Not required |
| Kind gating | None | Kind 1 only for low trust |
| Backfill | Not supported | Free for high-trust old events |
| Load predictability | Bursty, daily spikes | Smooth, predictable |

---

## 10) Configuration parameters

### 10.1 Trust thresholds

- `MID_THRESHOLD` (default: 0.5): trust score above which all kinds are allowed
- `HIGH_THRESHOLD` (default: 0.9): trust score above which backfill is free and max rate applies

### 10.2 Rank-queue IP limiter

- `RANK_QUEUE_IP_DAILY_LIMIT` (default: 100): max rank refresh requests per day per IP group

### 10.3 Rank cache

- `StaleThreshold`: 24 hours (ranks older than this are enqueued for refresh)
- `MaxRefreshInterval`: 7 days (ranks older than this are evicted)
- `MaxPubkeysToRank`: 1000 (batch size for rank refresh)

### 10.4 Token bucket

- `TimeToLive`: 1 hour (buckets inactive this long are eligible for deletion)
- `CleanupInterval`: 24 hours (how often cleanup runs)

### 10.5 ContextVM/Relatr

- `RELATR_RELAY` (default: wss://relay.contextvm.org): relay URL for rank lookups
- `RELATR_PUBKEY`: Relatr service pubkey
- `RELATR_SECRET_KEY`: Secret key for signing rank requests (generated if not provided)

---

## 11) Implementation references

- Event handling flow: [`handleEvent()`](main.go:162)
- Daily rate calculation: [`calculateDailyRate()`](main.go:206)
- Token bucket mechanics: [`Limiter`](rate.go:9), [`Bucket`](rate.go:19)
- Refill algorithm: [`refillLocked()`](rate.go:117)
- Rank cache: [`RankCache`](rank.go:22)
- Rank lookup with IP gating: [`lookupRank()`](main.go:227)
- Refresh batching: [`refresher()`](rank.go:271)
- ContextVM integration: [`refreshBatch()`](rank.go:324)
