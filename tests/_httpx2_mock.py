"""A small respx-shaped router over `httpx2.MockTransport`.

Exists because respx cannot mock `httpx2`: it type-checks responses with
`isinstance(return_value, httpx.Response)`, so using it would keep plain
`httpx` in the dependency tree purely for tests. This module replaces it and
lets `httpx` go entirely.

The surface intentionally mirrors the part of respx this suite actually used —
`mock.get(path).mock(return_value=...)`, `route.call_count`,
`route.calls[i].request`, `side_effect` lists, `base_url=` and
`assert_all_called=` — so the ~130 existing call sites read unchanged.

Interception works differently from respx, which patches transports globally.
Here `conftest.chat_client` builds its `ChatClient` around `dispatch_transport()`,
and that transport looks up whichever router is active when the request runs.
Same ergonomics at the call site; no monkeypatching of library internals.
"""

from __future__ import annotations

import re
from collections.abc import Iterator, Sequence
from contextlib import contextmanager
from contextvars import ContextVar
from dataclasses import dataclass, field
from typing import Any

import httpx2


class UnmockedRequest(BaseException):
    """A request the router cannot answer.

    Deliberately a `BaseException`: handlers under test catch broad
    `except Exception` to degrade gracefully, which would turn a mocking
    mistake into a passing test that silently exercised the failure path.
    """


_ACTIVE: ContextVar[MockRouter | None] = ContextVar("_ACTIVE", default=None)


@dataclass(frozen=True)
class Call:
    """One matched request/response pair, in respx's `route.calls[i]` shape."""

    request: httpx2.Request
    response: httpx2.Response | None


class CallList(list):
    """`route.calls` with respx's `.last` accessor."""

    @property
    def last(self) -> Call:
        return self[-1]


@dataclass
class Route:
    method: str
    url: str
    kind: str = "exact"
    base: str = ""
    _responses: list[Any] = field(default_factory=list)
    _cursor: int = 0
    calls: CallList = field(default_factory=CallList)

    def mock(
        self,
        return_value: httpx2.Response | None = None,
        side_effect: Sequence[Any] | BaseException | None = None,
    ) -> Route:
        """Configure the reply. `side_effect` entries are consumed in order.

        An entry may be a `Response` or an exception instance; exceptions are
        raised, matching respx and letting tests drive transport failures.
        """
        if return_value is not None and side_effect is not None:
            raise ValueError("pass return_value or side_effect, not both")
        if return_value is not None:
            if not isinstance(return_value, httpx2.Response):
                raise TypeError(f"{return_value!r} is not an httpx2.Response")
            self._responses = [return_value]
        elif side_effect is not None:
            # respx accepts a bare exception as well as a sequence.
            self._responses = (
                list(side_effect) if isinstance(side_effect, (list, tuple)) else [side_effect]
            )
        return self

    @property
    def call_count(self) -> int:
        return len(self.calls)

    @property
    def called(self) -> bool:
        return bool(self.calls)

    def _next(self) -> Any:
        if not self._responses:
            # BaseException, not AssertionError: the code under test wraps
            # broad `except Exception`, which would swallow this and report
            # a generic tool error instead of the mocking mistake.
            raise UnmockedRequest(f"{self.method} {self.url} was called but no response was mocked")
        # Last entry repeats, so a single return_value serves any number of
        # calls — the retry tests depend on a short side_effect list running
        # out and then holding.
        item = self._responses[min(self._cursor, len(self._responses) - 1)]
        self._cursor += 1
        return item


class MockRouter:
    """Collects routes and answers requests that match them."""

    def __init__(self, *, base_url: str | None = None, assert_all_called: bool = True) -> None:
        self._base = (base_url or "").rstrip("/")
        self._assert_all_called = assert_all_called
        self.routes: list[Route] = []

    def _add(
        self,
        method: str,
        url: str | None = None,
        *,
        url__startswith: str | None = None,
        url__regex: str | None = None,
    ) -> Route:
        """Register a route. Mirrors the three respx lookups this suite uses."""
        given = [v for v in (url, url__startswith, url__regex) if v is not None]
        if len(given) != 1:
            raise TypeError("pass exactly one of url, url__startswith, url__regex")
        if url__regex is not None:
            # Anchor to base_url when the pattern does not name a host itself,
            # so a route cannot answer for an unrelated origin. respx merged
            # base_url into every route regardless of lookup kind; without
            # this, a regressed caller aiming at the wrong base still matched.
            route = Route(
                method=method.upper(),
                url=url__regex,
                kind="regex",
                base="" if url__regex.startswith(("http://", "https://")) else self._base,
            )
        elif url__startswith is not None:
            route = Route(
                method=method.upper(),
                url=self._absolute(url__startswith)
                if not url__startswith.startswith(("http://", "https://"))
                else url__startswith,
                kind="startswith",
            )
        else:
            assert url is not None  # narrowed by the exactly-one check above
            route = Route(method=method.upper(), url=self._absolute(url), kind="exact")
        self.routes.append(route)
        return route

    def _absolute(self, url: str) -> str:
        if url.startswith("http://") or url.startswith("https://"):
            return url
        return f"{self._base}{url if url.startswith('/') else '/' + url}"

    def get(self, url: str | None = None, **lookups: str) -> Route:
        return self._add("GET", url, **lookups)

    def post(self, url: str | None = None, **lookups: str) -> Route:
        return self._add("POST", url, **lookups)

    def put(self, url: str | None = None, **lookups: str) -> Route:
        return self._add("PUT", url, **lookups)

    def patch(self, url: str | None = None, **lookups: str) -> Route:
        return self._add("PATCH", url, **lookups)

    def delete(self, url: str | None = None, **lookups: str) -> Route:
        return self._add("DELETE", url, **lookups)

    def resolve(self, request: httpx2.Request) -> httpx2.Response:
        """Find the route for `request`, record the call, and produce its reply."""
        target = str(request.url).split("?", 1)[0]
        for route in self.routes:
            if route.method != request.method:
                continue
            if not _matches(route, target):
                continue
            # Record BEFORE resolving. A route registered without `.mock()`
            # raises here, and if that happened first the call was never
            # recorded — so `assert not route.called` passed for a request
            # that HAD been made. Several of those guard security properties
            # ("nothing may be deleted on an unverified match"), and
            # `invoke_tool`'s `except Exception` converted the raised
            # AssertionError into a ToolError the test was already expecting.
            call = Call(request=request, response=None)
            route.calls.append(call)
            item = route._next()
            if isinstance(item, BaseException):
                raise item
            route.calls[-1] = Call(request=request, response=item)
            return item
        raise UnmockedRequest(f"unmocked request: {request.method} {target}")

    def _check_all_called(self) -> None:
        if not self._assert_all_called:
            return
        missed = [f"{r.method} {r.url}" for r in self.routes if not r.calls]
        if missed:
            raise AssertionError("routes registered but never called: " + ", ".join(missed))


def _matches(route: Route, target: str) -> bool:
    if route.kind == "regex":
        if route.base and not target.startswith(route.base):
            return False
        return re.search(route.url, target) is not None
    if route.kind == "startswith":
        return target.startswith(route.url)
    if route.url.endswith("*"):
        return target.startswith(route.url[:-1])
    return route.url == target


@contextmanager
def mock_api(
    *, base_url: str | None = None, assert_all_called: bool = True
) -> Iterator[MockRouter]:
    """Activate a router for the duration of the block. Not reentrant by design.

    Nesting would make "which router answers this request" ambiguous, and no
    test needs it; a nested use raises rather than silently shadowing.
    """
    if _ACTIVE.get() is not None:
        raise RuntimeError("mock_api() is already active; nested routers are not supported")
    router = MockRouter(base_url=base_url, assert_all_called=assert_all_called)
    token = _ACTIVE.set(router)
    try:
        yield router
        router._check_all_called()
    finally:
        _ACTIVE.reset(token)


def dispatch_transport() -> httpx2.MockTransport:
    """Transport that defers to whichever router is active at request time."""

    def handler(request: httpx2.Request) -> httpx2.Response:
        router = _ACTIVE.get()
        if router is None:
            raise UnmockedRequest(
                f"HTTP request with no active mock_api(): {request.method} {request.url}"
            )
        return router.resolve(request)

    return httpx2.MockTransport(handler)


@contextmanager
def intercept_all_clients() -> Iterator[None]:
    """Route every httpx2 client built without an explicit transport here.

    Transport injection alone is not enough: code under test builds its own
    clients — `ChatClient` inside the HTTPS app's lifespan, `_doctor_client`
    in the stdio doctor — and those would otherwise reach the real network.
    respx got this for free by patching globally; this is the equivalent.

    Three things are covered deliberately, because two of them were missed the
    first time and let tests issue real requests to Google:

    * the **sync** `Client` as well as `AsyncClient`;
    * `mounts=`, which routes per-pattern and would bypass `transport=`
      entirely — any mount is dropped when we supply the transport;
    * a client that names its own transport is still left alone, which is what
      keeps the ASGI integration harness working.
    """
    originals = {cls: cls.__init__ for cls in (httpx2.AsyncClient, httpx2.Client)}

    def make(cls: type, original: Any) -> Any:
        def patched(self: Any, *args: Any, **kwargs: Any) -> None:
            if kwargs.get("transport") is None:
                kwargs["transport"] = dispatch_transport()
                # A mount would win over `transport` for matching patterns and
                # send the request to the real network.
                kwargs.pop("mounts", None)
            original(self, *args, **kwargs)

        return patched

    for cls, original in originals.items():
        cls.__init__ = make(cls, original)
    try:
        yield
    finally:
        for cls, original in originals.items():
            # ty cannot reconcile the sync and async clients' distinct
            # __init__ signatures through the shared loop variable; the
            # restore is exactly the object we captured.
            cls.__init__ = original  # ty: ignore[invalid-assignment]
