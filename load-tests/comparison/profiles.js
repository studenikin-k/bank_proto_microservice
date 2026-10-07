// Shared temporal profiles. All rates are HTTP operations per second.
export const CAPACITY_RATES = [500, 750, 1000, 1500, 2500, 3000, 3500];
export const PROFILES = {
    stress: [
        { name: 'baseline', seconds: 60, target: 500 },
        { name: 'ramp_medium', seconds: 30, target: 1500 },
        { name: 'medium', seconds: 90, target: 1500 },
        { name: 'ramp_high', seconds: 30, target: 2500 },
        { name: 'high', seconds: 90, target: 2500 },
        { name: 'ramp_peak', seconds: 30, target: 3500 },
        { name: 'peak', seconds: 120, target: 3500 },
        { name: 'ramp_recovery', seconds: 10, target: 500 },
        { name: 'recovery_start', seconds: 80, target: 500 },
        { name: 'recovery_tail', seconds: 60, target: 500 },
    ],
    spike: [
        { name: 'baseline', seconds: 45, target: 500 },
        { name: 'jump', seconds: 1, target: 3500 },
        { name: 'peak', seconds: 30, target: 3500 },
        { name: 'fall', seconds: 1, target: 500 },
        { name: 'recovery_start', seconds: 73, target: 500 },
        { name: 'recovery_tail', seconds: 30, target: 500 },
    ],
};
export function phasePlan(kind, rate, duration) {
    const stages = kind === 'capacity' ? [{ name: 'steady', seconds: duration, target: rate }] : PROFILES[kind];
    if (!stages) throw new Error('unsupported test kind');
    let time = 0, previous = kind === 'capacity' ? rate : 500;
    return stages.map((s) => {
        const phase = { ...s, start_seconds: time, end_seconds: time + s.seconds,
            start_rate: previous, planned_iterations: (previous + s.target) * s.seconds / 2 };
        time += s.seconds;
        previous = s.target;
        return phase;
    });
}
export function phaseAt(phases, seconds) {
    return phases.find((p) => seconds < p.end_seconds) || { name: 'drain', end_seconds: Infinity };
}
