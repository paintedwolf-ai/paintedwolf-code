// Terrain grid and level loading. Levels are pure text data in → sim
// state out: no time- or environment-dependent input (ARCHITECTURE.md,
// "Determinism rules").

public enum Cell: Equatable, Sendable {
    case empty
    case solid
    case hazard
    case water
    case exit
}

public struct TerrainGrid: Sendable {
    public let width: Int
    public let height: Int
    public private(set) var cells: [Cell]

    public init(width: Int, height: Int, fill: Cell = .empty) {
        self.width = width
        self.height = height
        self.cells = Array(repeating: fill, count: width * height)
    }

    @inline(__always)
    public func cell(at c: LCoord) -> Cell {
        guard c.x >= 0, c.x < width, c.y >= 0, c.y < height else { return .empty }
        return cells[c.y * width + c.x]
    }

    public mutating func setCell(_ cell: Cell, at c: LCoord) {
        guard c.x >= 0, c.x < width, c.y >= 0, c.y < height else { return }
        cells[c.y * width + c.x] = cell
    }

    /// Out-of-bounds counts as open air (lemmings walking off the edge fall).
    public func isSolid(at c: LCoord) -> Bool { cell(at: c) == .solid }

    /// A chasm column: no ground for `depth` cells below `at`.
    public func isChasm(at c: LCoord, depth: Int = 3) -> Bool {
        (1...depth).allSatisfy { offset in !isSolid(at: LCoord(x: c.x, y: c.y + offset)) }
    }
}

public struct SpawnPoint: Sendable {
    public var position: LCoord
    /// Ticks between consecutive spawns.
    public var interval: Int
    public var count: Int

    public init(position: LCoord, interval: Int, count: Int) {
        self.position = position
        self.interval = interval
        self.count = count
    }
}

public struct Level: Sendable {
    public var name: String
    public var grid: TerrainGrid
    public var spawn: SpawnPoint

    public init(name: String, grid: TerrainGrid, spawn: SpawnPoint) {
        self.name = name
        self.grid = grid
        self.spawn = spawn
    }
}

public enum LevelLoadingError: Error, Equatable, Sendable {
    case malformedLine(String)
    case missingName
    case missingRows
    case inconsistentWidth(line: Int)
    case invalidDimension(String)
    case missingSpawnPoint
}

/// Text level format:
///
///     name: the-pit
///     spawn: 2 1 60 10        # x y interval count
///     rows follow after a "rows:" marker, one line per grid row.
///     row glyphs: '.' empty, '#' solid, '^' hazard, '~' water, 'X' exit
public enum LevelLoader {
    public static func load(text: String, fallbackName: String = "unnamed") throws -> Level {
        var name: String?
        var spawn: SpawnPoint?
        var rows: [String] = []
        var inRows = false

        for (index, raw) in text.split(separator: "\n", omittingEmptySubsequences: false).enumerated() {
            let line = raw.trimmingCharacters(in: .whitespaces)
            if line.isEmpty || line.hasPrefix("#") { continue }

            if inRows {
                rows.append(line)
                continue
            }

            let parts = line.split(separator: " ", omittingEmptySubsequences: true).map(String.init)
            switch parts[0] {
            case "name:":
                name = line.dropFirst("name:".count).trimmingCharacters(in: .whitespaces)
            case "spawn:":
                guard parts.count == 5,
                      let x = Int(parts[1]), let y = Int(parts[2]),
                      let interval = Int(parts[3]), let count = Int(parts[4])
                else { throw LevelLoadingError.malformedLine(line) }
                spawn = SpawnPoint(position: LCoord(x: x, y: y), interval: interval, count: count)
            case "rows:":
                inRows = true
            default:
                throw LevelLoadingError.malformedLine(line)
            }
            _ = index
        }

        guard let levelName = name else { throw LevelLoadingError.missingName }
        guard !rows.isEmpty else { throw LevelLoadingError.missingRows }
        guard let spawnPoint = spawn else { throw LevelLoadingError.missingSpawnPoint }

        let width = rows[0].count
        for (i, row) in rows.enumerated() where row.count != width {
            throw LevelLoadingError.inconsistentWidth(line: i)
        }

        var grid = TerrainGrid(width: width, height: rows.count)
        for (y, row) in rows.enumerated() {
            for (x, glyph) in row.enumerated() {
                let cell: Cell
                switch glyph {
                case ".": cell = .empty
                case "#": cell = .solid
                case "^": cell = .hazard
                case "~": cell = .water
                case "X": cell = .exit
                default: throw LevelLoadingError.malformedLine(row)
                }
                grid.setCell(cell, at: LCoord(x: x, y: y))
            }
        }

        return Level(name: levelName, grid: grid, spawn: spawnPoint)
    }

    public static func load(named levelName: String) throws -> Level {
        guard let text = BuiltInLevels.all[levelName] else {
            throw LevelLoadingError.malformedLine("unknown built-in level: \(levelName)")
        }
        return try load(text: text)
    }
}

enum BuiltInLevels {
    static let all: [String: String] = [
        "the-pit": """
        name: the-pit
        spawn: 2 1 30 4
        rows:
        ............................
        ..#......................X..
        ..#......................XX.
        ..#......................XX.
        ..#..........#...........XX.
        ..#......................XX.
        ..######...........########.
        ............................
        ............................
        ############################
        """,

        "stairway": """
        name: stairway
        spawn: 1 1 40 3
        rows:
        ..............................
        .#..........................X
        .#......................#####X
        .#...............###....#...X
        .#.........###..........#...X
        .#...###.................#...X
        .#.......................#...X
        .#########################..X
        ..............................
        ..............................
        """,
    ]
}
