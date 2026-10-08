//! Critically damped motion for notched wheel input.
//! Nearby notches extend the destination while preserving velocity.

use serde::Serialize;

/// Matches the native wheel-line distance (`Scrollbar::pixelsPerLineStep`).
pub const PIXELS_PER_LINE: f64 = 40.0;
/// Natural frequency in seconds⁻¹; peak speed from rest is `distance × rate / e`.
const SPRING_RATE: f64 = 32.0;
/// Remaining distance below which a glide may land.
const LAND_PT: f64 = 0.5;
/// Speed below which a glide may land, points per second.
const LAND_SPEED_PT_S: f64 = 20.0;
/// Notches within this distance of a glide's origin extend it.
const RETARGET_RADIUS_PT: f64 = 24.0;
/// Concurrent glides; beyond this the oldest lands at once.
const MAX_GLIDES: usize = 4;

#[derive(Clone, Copy, Debug, Default, PartialEq)]
pub struct Vector {
    pub x: f64,
    pub y: f64,
}

impl Vector {
    fn plus(self, other: Vector) -> Vector {
        Vector { x: self.x + other.x, y: self.y + other.y }
    }

    fn minus(self, other: Vector) -> Vector {
        Vector { x: self.x - other.x, y: self.y - other.y }
    }

    fn largest_axis(self) -> f64 {
        self.x.abs().max(self.y.abs())
    }
}

/// Advances one axis of the approach by `elapsed` seconds from `remaining`
/// points short of the destination at `velocity` points per second toward it.
/// Returns the new remaining distance and velocity.
fn approach(remaining: f64, velocity: f64, elapsed: f64) -> (f64, f64) {
    let decay = (-SPRING_RATE * elapsed).exp();
    let drive = SPRING_RATE * remaining - velocity;
    (
        (remaining + drive * elapsed) * decay,
        (velocity + SPRING_RATE * drive * elapsed) * decay,
    )
}

/// Where a notch landed: a window and a point in its coordinates.
#[derive(Clone, Copy, Debug, PartialEq)]
pub struct Origin {
    pub window: isize,
    pub x: f64,
    pub y: f64,
}

impl Origin {
    fn near(&self, other: &Origin) -> bool {
        self.window == other.window
            && (self.x - other.x).hypot(self.y - other.y) <= RETARGET_RADIUS_PT
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub enum GlideOutcome {
    Landed,
    Interrupted,
}

/// One finished glide, for scroll diagnostics.
#[derive(Clone, Debug, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct GlideReport {
    pub requested_x: f64,
    pub requested_y: f64,
    pub emitted_x: i64,
    pub emitted_y: i64,
    pub notches: u32,
    pub duration_ms: f64,
    pub outcome: GlideOutcome,
}

struct Glide<T> {
    origin: Origin,
    template: T,
    requested: Vector,
    remaining: Vector,
    velocity: Vector,
    /// Sub-point distance owed to the next emission.
    carry: Vector,
    emitted: (i64, i64),
    notches: u32,
    started_at: f64,
    last_tick: f64,
    land_now: bool,
}

impl<T> Glide<T> {
    fn report(&self, now: f64, outcome: GlideOutcome) -> GlideReport {
        GlideReport {
            requested_x: self.requested.x,
            requested_y: self.requested.y,
            emitted_x: self.emitted.0,
            emitted_y: self.emitted.1,
            notches: self.notches,
            duration_ms: (now - self.started_at) * 1000.0,
            outcome,
        }
    }

    /// Moves the glide on by `elapsed` seconds; returns the step and whether
    /// the glide landed.
    fn advance(&mut self, elapsed: f64) -> (Vector, bool) {
        let (remaining_x, velocity_x) = approach(self.remaining.x, self.velocity.x, elapsed);
        let (remaining_y, velocity_y) = approach(self.remaining.y, self.velocity.y, elapsed);
        let remaining = Vector { x: remaining_x, y: remaining_y };
        let velocity = Vector { x: velocity_x, y: velocity_y };
        let landing = self.land_now
            || (remaining.largest_axis() < LAND_PT && velocity.largest_axis() < LAND_SPEED_PT_S);
        if landing {
            let step = self.remaining;
            self.remaining = Vector::default();
            self.velocity = Vector::default();
            return (step, true);
        }
        let step = self.remaining.minus(remaining);
        self.remaining = remaining;
        self.velocity = velocity;
        (step, false)
    }
}

/// Active glides, each carrying the event template its emissions copy.
pub struct Glides<T> {
    active: Vec<Glide<T>>,
}

impl<T> Default for Glides<T> {
    fn default() -> Self {
        Self { active: Vec::new() }
    }
}

impl<T> Glides<T> {
    pub fn is_idle(&self) -> bool {
        self.active.is_empty()
    }

    /// Adds a notch of `delta` points at `now` seconds.
    pub fn notch(&mut self, origin: Origin, delta: Vector, now: f64, template: T) {
        if let Some(glide) = self.active.iter_mut().find(|glide| glide.origin.near(&origin)) {
            glide.requested = glide.requested.plus(delta);
            glide.remaining = glide.remaining.plus(delta);
            glide.notches += 1;
            return;
        }
        if self.active.len() >= MAX_GLIDES {
            if let Some(oldest) = self.active.iter_mut().find(|glide| !glide.land_now) {
                oldest.land_now = true;
            }
        }
        self.active.push(Glide {
            origin,
            template,
            requested: delta,
            remaining: delta,
            velocity: Vector::default(),
            carry: Vector::default(),
            emitted: (0, 0),
            notches: 1,
            started_at: now,
            last_tick: now,
            land_now: false,
        });
    }

    /// Advances every glide to `now`, emitting whole-point deltas.
    pub fn tick(&mut self, now: f64, mut emit: impl FnMut(&T, i32, i32)) -> Vec<GlideReport> {
        let mut reports = Vec::new();
        self.active.retain_mut(|glide| {
            let elapsed = (now - glide.last_tick).max(0.0);
            glide.last_tick = now;
            let (step, landing) = glide.advance(elapsed);
            let exact = glide.carry.plus(step);
            let (dx, dy) = (exact.x.round(), exact.y.round());
            glide.carry = Vector { x: exact.x - dx, y: exact.y - dy };
            if dx != 0.0 || dy != 0.0 {
                emit(&glide.template, dx as i32, dy as i32);
                glide.emitted.0 += dx as i64;
                glide.emitted.1 += dy as i64;
            }
            if landing {
                reports.push(glide.report(now, GlideOutcome::Landed));
            }
            !landing
        });
        reports
    }

    /// Stops every glide where it is.
    pub fn interrupt(&mut self, now: f64) -> Vec<GlideReport> {
        self.active
            .drain(..)
            .map(|glide| glide.report(now, GlideOutcome::Interrupted))
            .collect()
    }
}

/// What happens to one wheel event.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum WheelRoute {
    /// Replace the event with a glide.
    Glide,
    Pass,
    /// Pass the event and stop active glides where they are.
    PassAndInterrupt,
}

/// Facts about one wheel event that decide its route.
#[derive(Clone, Copy, Debug, Default)]
pub struct WheelFacts {
    /// The device reports pixel deltas (trackpad, Magic Mouse, momentum).
    pub precise: bool,
    /// A trackpad touch is beginning.
    pub touch_began: bool,
    /// Control, Option, or Command is held.
    pub zoom_modifier: bool,
    pub reduce_motion: bool,
    pub over_web_content: bool,
}

pub fn route(facts: &WheelFacts) -> WheelRoute {
    if facts.precise {
        return if facts.touch_began {
            WheelRoute::PassAndInterrupt
        } else {
            WheelRoute::Pass
        };
    }
    if facts.zoom_modifier || facts.reduce_motion || !facts.over_web_content {
        return WheelRoute::Pass;
    }
    WheelRoute::Glide
}

#[cfg(test)]
mod tests {
    use super::*;

    const FRAME: f64 = 1.0 / 120.0;

    fn origin(x: f64) -> Origin {
        Origin { window: 1, x, y: 100.0 }
    }

    fn down(points: f64) -> Vector {
        Vector { x: 0.0, y: points }
    }

    struct Run {
        emissions: Vec<(u8, i32, i32)>,
        reports: Vec<GlideReport>,
    }

    impl Run {
        fn total(&self, template: u8) -> (i64, i64) {
            self.emissions
                .iter()
                .filter(|(id, _, _)| *id == template)
                .fold((0, 0), |(x, y), (_, dx, dy)| (x + *dx as i64, y + *dy as i64))
        }
    }

    /// Ticks at display rate from `from` until idle or `until`.
    fn run(glides: &mut Glides<u8>, from: f64, until: f64, out: &mut Run) -> f64 {
        let mut now = from;
        while !glides.is_idle() && now < until {
            now += FRAME;
            let reports = glides.tick(now, |id, dx, dy| out.emissions.push((*id, dx, dy)));
            out.reports.extend(reports);
        }
        now
    }

    /// The vertical distance emitted on the frame after `now`.
    fn next_step(glides: &mut Glides<u8>, now: f64) -> i32 {
        let mut step = 0;
        glides.tick(now + FRAME, |_, _, dy| step += dy);
        step
    }

    fn new_run() -> Run {
        Run { emissions: Vec::new(), reports: Vec::new() }
    }

    #[test]
    fn a_notch_lands_its_whole_distance_over_several_frames() {
        let mut glides = Glides::default();
        let mut out = new_run();
        glides.notch(origin(0.0), down(40.0), 0.0, 0);
        run(&mut glides, 0.0, 1.0, &mut out);

        assert_eq!(out.total(0), (0, 40));
        assert!(out.emissions.len() >= 8, "a notch should spread over frames");
        let report = &out.reports[0];
        assert_eq!(report.outcome, GlideOutcome::Landed);
        assert_eq!(report.emitted_y, 40);
        assert!(report.duration_ms > 100.0 && report.duration_ms < 250.0);
    }

    #[test]
    fn the_first_frame_moves_part_of_the_notch() {
        let mut glides = Glides::default();
        let mut first = None;
        glides.notch(origin(0.0), down(40.0), 0.0, 0);
        glides.tick(FRAME, |_, _, dy| first = Some(dy));

        let first = first.expect("the first frame emits");
        assert!(first > 0 && first < 40);
    }

    #[test]
    fn motion_eases_in_from_rest() {
        let mut glides = Glides::default();
        glides.notch(origin(0.0), down(400.0), 0.0, 0);
        let steps: Vec<i32> =
            (0..3).map(|frame| next_step(&mut glides, f64::from(frame) * FRAME)).collect();

        assert!(steps[0] < steps[1] && steps[1] < steps[2], "steps {steps:?} should grow");
    }

    #[test]
    fn peak_speed_stays_under_distance_times_rate_over_e() {
        let mut glides = Glides::default();
        let mut out = new_run();
        let distance = 1200.0;
        glides.notch(origin(0.0), down(distance), 0.0, 0);
        run(&mut glides, 0.0, 2.0, &mut out);

        let bound = distance * SPRING_RATE / std::f64::consts::E * FRAME;
        let peak = out.emissions.iter().map(|(_, _, dy)| f64::from(*dy)).fold(0.0, f64::max);
        assert!(peak <= bound + 1.0, "peak frame {peak} exceeds {bound:.1}");
        assert!(peak >= bound * 0.9, "peak frame {peak} far below {bound:.1}");
    }

    #[test]
    fn a_notch_during_a_glide_adds_acceleration_not_a_jump() {
        let mut glides = Glides::default();
        let mut out = new_run();
        let delta = 400.0;
        glides.notch(origin(0.0), down(delta), 0.0, 0);
        let now = run(&mut glides, 0.0, 0.05, &mut out);
        let before = out.emissions.last().map(|(_, _, dy)| *dy).expect("moving");
        glides.notch(origin(0.0), down(delta), now, 0);
        let after = next_step(&mut glides, now);

        // One frame of extra spring force moves at most half of rate² × Δ × dt².
        let pull = 0.5 * (SPRING_RATE * FRAME).powi(2) * delta;
        assert!(
            f64::from(after - before) <= pull.ceil() + 1.0,
            "step went {before} → {after}; a speed jump, not acceleration"
        );
    }

    #[test]
    fn a_nearby_notch_extends_the_glide() {
        let mut glides = Glides::default();
        let mut out = new_run();
        glides.notch(origin(0.0), down(40.0), 0.0, 0);
        let now = run(&mut glides, 0.0, 0.05, &mut out);
        glides.notch(origin(10.0), down(40.0), now, 1);
        run(&mut glides, now, 1.0, &mut out);

        assert_eq!(out.total(0), (0, 80));
        assert_eq!(out.reports.len(), 1);
        assert_eq!(out.reports[0].notches, 2);
    }

    #[test]
    fn a_reversal_takes_back_distance() {
        let mut glides = Glides::default();
        let mut out = new_run();
        glides.notch(origin(0.0), down(40.0), 0.0, 0);
        let now = run(&mut glides, 0.0, 0.03, &mut out);
        glides.notch(origin(0.0), down(-40.0), now, 0);
        run(&mut glides, now, 1.0, &mut out);

        assert_eq!(out.total(0), (0, 0));
    }

    #[test]
    fn fractional_notches_keep_their_distance() {
        let mut glides = Glides::default();
        let mut out = new_run();
        let mut now = 0.0;
        for _ in 0..25 {
            glides.notch(origin(0.0), down(4.000_244), now, 0);
            now = run(&mut glides, now, now + 0.02, &mut out);
        }
        run(&mut glides, now, now + 1.0, &mut out);

        assert_eq!(out.total(0), (0, 100));
    }

    #[test]
    fn a_distant_notch_glides_on_its_own() {
        let mut glides = Glides::default();
        let mut out = new_run();
        glides.notch(origin(0.0), down(40.0), 0.0, 0);
        glides.notch(origin(200.0), down(-80.0), 0.0, 1);
        run(&mut glides, 0.0, 1.0, &mut out);

        assert_eq!(out.total(0), (0, 40));
        assert_eq!(out.total(1), (0, -80));
        assert_eq!(out.reports.len(), 2);
    }

    #[test]
    fn another_window_glides_on_its_own() {
        let mut glides = Glides::default();
        let mut out = new_run();
        glides.notch(origin(0.0), down(40.0), 0.0, 0);
        glides.notch(Origin { window: 2, ..origin(0.0) }, down(40.0), 0.0, 1);
        run(&mut glides, 0.0, 1.0, &mut out);

        assert_eq!(out.reports.len(), 2);
    }

    #[test]
    fn both_axes_move_together() {
        let mut glides = Glides::default();
        let mut out = new_run();
        glides.notch(origin(0.0), Vector { x: 40.0, y: -80.0 }, 0.0, 0);
        run(&mut glides, 0.0, 1.0, &mut out);

        assert_eq!(out.total(0), (40, -80));
    }

    #[test]
    fn an_interrupt_stops_the_glide_where_it_is() {
        let mut glides = Glides::default();
        let mut out = new_run();
        glides.notch(origin(0.0), down(400.0), 0.0, 0);
        let now = run(&mut glides, 0.0, 3.0 * FRAME, &mut out);
        let reports = glides.interrupt(now);
        let moved = out.total(0).1;
        run(&mut glides, now, 1.0, &mut out);

        assert!(glides.is_idle());
        assert_eq!(out.total(0).1, moved, "nothing moves after an interrupt");
        assert!(moved > 0 && moved < 400);
        assert_eq!(reports[0].outcome, GlideOutcome::Interrupted);
        assert_eq!(reports[0].emitted_y, moved);
    }

    #[test]
    fn the_cap_lands_the_oldest_glide_at_once() {
        let mut glides = Glides::default();
        for (index, x) in [0.0, 100.0, 200.0, 300.0, 400.0].into_iter().enumerate() {
            glides.notch(origin(x), down(40.0), 0.0, index as u8);
        }
        let mut first = 0;
        let reports = glides.tick(FRAME, |id, _, dy| {
            if *id == 0 {
                first += dy;
            }
        });

        assert_eq!(first, 40);
        assert_eq!(reports.len(), 1);
    }

    #[test]
    fn a_stalled_frame_catches_up_by_elapsed_time() {
        let mut glides = Glides::default();
        let mut moved = 0;
        glides.notch(origin(0.0), down(40.0), 0.0, 0);
        let reports = glides.tick(0.3, |_, _, dy| moved += dy);

        assert_eq!(moved, 40);
        assert_eq!(reports.len(), 1);
    }

    #[test]
    fn reports_use_camel_case_keys() {
        let report = GlideReport {
            requested_x: 0.0,
            requested_y: 40.0,
            emitted_x: 0,
            emitted_y: 40,
            notches: 1,
            duration_ms: 180.0,
            outcome: GlideOutcome::Landed,
        };
        let json = serde_json::to_value(&report).expect("serialize");

        assert_eq!(json["requestedY"], 40.0);
        assert_eq!(json["durationMs"], 180.0);
        assert_eq!(json["outcome"], "landed");
    }

    #[test]
    fn precise_input_passes_through() {
        let facts = WheelFacts { precise: true, over_web_content: true, ..WheelFacts::default() };
        assert_eq!(route(&facts), WheelRoute::Pass);
    }

    #[test]
    fn a_trackpad_touch_stops_glides() {
        let facts = WheelFacts { precise: true, touch_began: true, ..WheelFacts::default() };
        assert_eq!(route(&facts), WheelRoute::PassAndInterrupt);
    }

    #[test]
    fn a_notch_over_web_content_glides() {
        let facts = WheelFacts { over_web_content: true, ..WheelFacts::default() };
        assert_eq!(route(&facts), WheelRoute::Glide);
    }

    #[test]
    fn modified_reduced_or_native_wheels_pass_through() {
        let base = WheelFacts { over_web_content: true, ..WheelFacts::default() };
        for facts in [
            WheelFacts { zoom_modifier: true, ..base },
            WheelFacts { reduce_motion: true, ..base },
            WheelFacts { over_web_content: false, ..base },
        ] {
            assert_eq!(route(&facts), WheelRoute::Pass);
        }
    }
}
