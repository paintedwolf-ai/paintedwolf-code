package invocation

import (
	"context"
	"database/sql"
	"errors"
)

type activityCounts struct {
	total, running, waiting, failed, interrupted, refused, uncertain, effects int
	closed, utilityFailures, recovered                                        int
}

func (c *activityCounts) recoverUtilityExits() {
	c.failed -= c.utilityFailures
	c.recovered = c.utilityFailures
}

// Repetitions are classified in SQL without decoding execution bodies.
func readActivityCounts(ctx context.Context, tx *sql.Tx, program string) (groups []activityGroup, err error) {
	rows, err := tx.QueryContext(ctx, `WITH outcomes AS (
 SELECT ordinal,node_id,operation_id,scope_json,id,state,effect_certainty,consumer_closed,
 CASE
   WHEN state NOT IN ('completed','failed') THEN 0
   WHEN conflict_path<>'' OR stop_code<>'' OR host_stop_code<>'' THEN 1
   WHEN consumer_closed OR (state='completed' AND complete AND publication_eligible) THEN 0
   WHEN state='failed' OR COALESCE(status_code,0)<>0 THEN 1
   ELSE 0
 END AS failed,
 COALESCE(status_code,0)<>0 AND conflict_path='' AND stop_code='' AND host_stop_code='' AS utility_exit
 FROM invocation_program_executions WHERE program_id=?
 )
 SELECT min(ordinal),count(*),
 sum(state IN ('prepared','running')),sum(state='waiting'),sum(failed),
 sum(state='interrupted'),sum(state='refused'),
 sum(effect_certainty IN ('unknown','attempted')),sum(effect_certainty='completed'),
 sum(consumer_closed),sum(failed AND utility_exit)
 FROM outcomes
 GROUP BY node_id,operation_id,scope_json,CASE WHEN operation_id='program &' THEN id ELSE '' END ORDER BY min(ordinal)`, program)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var g activityGroup
		c := &g.counts
		if err := rows.Scan(&g.ordinal, &c.total, &c.running, &c.waiting, &c.failed, &c.interrupted, &c.refused, &c.uncertain, &c.effects, &c.closed, &c.utilityFailures); err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	return groups, rows.Err()
}
