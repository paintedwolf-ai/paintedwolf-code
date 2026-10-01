//
//  UserPrompt.swift
//  The snapshot half of the director prompt: bounded projection of
//  level, stats, features, and lemmings.
//

import HerdCore

enum UserPromptBuilder {
    /// Bounds keep the prompt from growing without limit on large levels.
    private static let maxFeatures = 20
    private static let maxLemmings = 40

    static func text(for snapshot: GameStateSnapshot, context: DirectiveContext) -> String {
        var lines: [String] = []
        lines.append("Level: \(snapshot.levelName) (tick \(snapshot.tick))")
        let s = snapshot.stats
        lines.append(
            "Stats: total=\(s.total) walkers=\(s.walkers) assigned=\(s.assigned) "
                + "safe=\(s.safe) lost=\(s.lost)"
        )

        if !snapshot.features.isEmpty {
            lines.append("Features:")
            for feature in snapshot.features.prefix(maxFeatures) {
                lines.append(
                    "- \(feature.kind.rawValue) at (\(feature.position.x), \(feature.position.y))"
                        + " size \(feature.size.x)x\(feature.size.y)"
                )
            }
        }

        lines.append("Lemmings:")
        for lemming in snapshot.lemmings.prefix(maxLemmings) {
            let assigned = lemming.assignedSkill?.rawValue ?? "none"
            lines.append(
                "- id \(lemming.id) \(lemming.state.rawValue)"
                    + " at (\(lemming.position.x), \(lemming.position.y)) assigned: \(assigned)"
            )
        }

        if let resolved = context.lastResolved {
            lines.append(
                "Previous batch: assigned=\(resolved.assigned) skipped=\(resolved.skipped)"
            )
            for note in resolved.notes { lines.append("Previous note: \(note)") }
        }
        for note in context.focusNotes { lines.append("Directive: \(note)") }

        return lines.joined(separator: "\n")
    }
}
