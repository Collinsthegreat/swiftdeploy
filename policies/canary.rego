package swiftdeploy.canary

import future.keywords.if
import future.keywords.contains

default allow := false

allow if {
    count(violations) == 0
}

violations contains msg if {
    error_rate_percent := to_number(input.error_rate_percent)
    error_rate_percent > data.thresholds.max_error_rate_percent
    msg := sprintf(
        "Error rate too high: %v%%, maximum allowed is %v%%",
        [error_rate_percent, data.thresholds.max_error_rate_percent]
    )
}

violations contains msg if {
    p99_latency_ms := to_number(input.p99_latency_ms)
    p99_latency_ms > data.thresholds.max_p99_latency_ms
    msg := sprintf(
        "P99 latency too high: %vms, maximum allowed is %vms",
        [p99_latency_ms, data.thresholds.max_p99_latency_ms]
    )
}

decision := {
    "allow": allow,
    "violations": violations,
    "domain": "canary",
    "action": input.action,
    "evaluated_at": input.timestamp,
}
