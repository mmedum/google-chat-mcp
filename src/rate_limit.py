"""Per-user in-memory token bucket.

60 calls/minute default; configurable via `GCM_RATE_LIMIT_PER_MINUTE`. Single
process — swap for Redis if this server is ever horizontally scaled.
"""

from __future__ import annotations

import asyncio
import time
from dataclasses import dataclass, field


@dataclass(slots=True)
class _Bucket:
    tokens: float
    updated_at: float


class TokenBucketLimiter:
    """Classic token bucket, refill rate = capacity / 60s.

    Buckets are opportunistically evicted when they go idle long enough to have
    fully refilled — those entries are indistinguishable from "not yet seen",
    so keeping them is pure memory waste.
    """

    _IDLE_EVICTION_SECONDS = 300.0

    def __init__(self, capacity: int, window_seconds: float = 60.0) -> None:
        if capacity <= 0:
            raise ValueError("capacity must be positive")
        self._capacity = float(capacity)
        self._refill_per_second = self._capacity / window_seconds
        self._buckets: dict[str, _Bucket] = {}
        self._lock = asyncio.Lock()

    async def allow(self, user_sub: str, now: float | None = None) -> bool:
        t = now if now is not None else time.monotonic()
        async with self._lock:
            self._evict_stale(t)
            bucket = self._buckets.get(user_sub)
            if bucket is None:
                self._buckets[user_sub] = _Bucket(tokens=self._capacity - 1.0, updated_at=t)
                return True
            elapsed = max(0.0, t - bucket.updated_at)
            bucket.tokens = min(self._capacity, bucket.tokens + elapsed * self._refill_per_second)
            bucket.updated_at = t
            if bucket.tokens >= 1.0:
                bucket.tokens -= 1.0
                return True
            return False

    def _evict_stale(self, now: float) -> None:
        cutoff = now - self._IDLE_EVICTION_SECONDS
        stale = [k for k, v in self._buckets.items() if v.updated_at < cutoff]
        for k in stale:
            del self._buckets[k]


@dataclass(slots=True)
class ActiveUserTracker:
    """Used to drive the `mcp_active_users` gauge. Stale entries expire."""

    window_seconds: float = 300.0
    _seen: dict[str, float] = field(default_factory=dict)
    _lock: asyncio.Lock = field(default_factory=asyncio.Lock)

    async def touch(self, user_sub: str) -> int:
        now = time.monotonic()
        async with self._lock:
            self._seen[user_sub] = now
            cutoff = now - self.window_seconds
            stale = [k for k, v in self._seen.items() if v < cutoff]
            for k in stale:
                del self._seen[k]
            return len(self._seen)
