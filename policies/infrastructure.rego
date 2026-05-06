package swiftdeploy.infrastructure

import future.keywords.if
import future.keywords.contains

default allow := false

allow if {
    count(violations) == 0
}

violations contains msg if {
    disk_free_gb := to_number(input.disk_free_gb)
    disk_free_gb < data.thresholds.min_disk_free_gb
    msg := sprintf(
        "Insufficient disk space: %v GB free, minimum required is %v GB",
        [disk_free_gb, data.thresholds.min_disk_free_gb]
    )
}

violations contains msg if {
    cpu_load := to_number(input.cpu_load)
    cpu_load > data.thresholds.max_cpu_load
    msg := sprintf(
        "CPU load too high: %v, maximum allowed is %v",
        [cpu_load, data.thresholds.max_cpu_load]
    )
}

decision := {
    "allow": allow,
    "violations": violations,
    "domain": "infrastructure",
    "action": input.action,
    "evaluated_at": input.timestamp,
}
