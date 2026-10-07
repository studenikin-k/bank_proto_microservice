// k6 arrival-rate executor progress uses Go time.Since(startTime).
// Unlike Date.now(), it does not follow wall-clock corrections.
// https://github.com/grafana/k6/blob/master/lib/executor/constant_arrival_rate.go
// https://github.com/grafana/k6/blob/master/lib/executor/ramping_arrival_rate.go
export function progressOffset(progress, seconds) {
    if (!Number.isFinite(progress) || progress < 0 || progress > 1)
        throw new Error('Invalid k6 executor progress');
    return progress * seconds * 1000;
}
export function completionClock(progress, seconds, startedOffset, timings) {
    const inWindow = progress < 1;
    const boundary = seconds * 1000;
    // Progress is capped at 1 during drain. HTTP blocked+duration is a lower
    // bound on post-window elapsed time, excluding JS validation/scheduling.
    const httpElapsed = Math.max(0, Number(timings.blocked) || 0) +
        Math.max(0, Number(timings.duration) || 0);
    return {
        inWindow,
        offset: inWindow ? progressOffset(progress, seconds) :
            Math.max(boundary, startedOffset + httpElapsed),
        drainEstimate: !inWindow,
    };
}
