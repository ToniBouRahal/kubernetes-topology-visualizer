"""One-minute bucketing and window arithmetic — ADR-005 D-5.4, test T-5.5.

`bucket_start` is the function that decides which row every observation lands in. It is the reason
the in-memory and PostgreSQL repositories agree, and the reason a restart cannot double-count.
It had no direct test: the requirements checklist audit in P5-T15 found the row unresolved, and
the function genuinely was uncovered.

The properties that matter are timezone correctness and idempotence. Bucketing that drifts by an
hour under a non-UTC input would corrupt every window query in a way no integration test would
obviously attribute to bucketing.
"""

from datetime import UTC, datetime, timedelta, timezone

import pytest

from app.domain.window import bucket_start


def utc(y, mo, d, h, mi, s=0, us=0) -> datetime:
    return datetime(y, mo, d, h, mi, s, us, tzinfo=UTC)


class TestBucketStart:
    def test_truncates_seconds_and_microseconds(self):
        assert bucket_start(utc(2026, 8, 22, 14, 37, 59, 999_999)) == utc(2026, 8, 22, 14, 37)

    def test_an_exact_minute_is_its_own_bucket(self):
        # A boundary instant must not fall into the previous bucket; windows are half-open and
        # lower-inclusive, so the start of a minute belongs to that minute.
        moment = utc(2026, 8, 22, 14, 37)
        assert bucket_start(moment) == moment

    def test_is_idempotent(self):
        once = bucket_start(utc(2026, 8, 22, 14, 37, 42))
        assert bucket_start(once) == once

    @pytest.mark.parametrize("second", [0, 1, 30, 58, 59])
    def test_every_second_in_a_minute_maps_to_the_same_bucket(self, second):
        assert bucket_start(utc(2026, 8, 22, 14, 37, second)) == utc(2026, 8, 22, 14, 37)

    def test_adjacent_seconds_across_a_boundary_land_in_different_buckets(self):
        before = bucket_start(utc(2026, 8, 22, 14, 37, 59, 999_999))
        after = bucket_start(utc(2026, 8, 22, 14, 38, 0))
        assert after - before == timedelta(minutes=1)

    def test_a_non_utc_input_is_converted_before_truncating(self):
        # The failure this guards: truncating in local time and then labelling the result UTC
        # shifts every bucket by the offset, which would silently corrupt every window query.
        plus_five_thirty = timezone(timedelta(hours=5, minutes=30))
        local = datetime(2026, 8, 22, 20, 7, 42, tzinfo=plus_five_thirty)  # 14:37:42Z
        assert bucket_start(local) == utc(2026, 8, 22, 14, 37)

    def test_the_result_is_always_utc(self):
        result = bucket_start(datetime(2026, 8, 22, 20, 7, 42, tzinfo=timezone(timedelta(hours=9))))
        assert result.tzinfo is not None
        assert result.utcoffset() == timedelta(0)

    def test_midnight_rollover(self):
        assert bucket_start(utc(2026, 8, 22, 23, 59, 59)) == utc(2026, 8, 22, 23, 59)
        assert bucket_start(utc(2026, 8, 23, 0, 0, 0)) == utc(2026, 8, 23, 0, 0)
