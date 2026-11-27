SELECT id, 'semi;colon' AS label FROM items WHERE note = 'a;b';
UPDATE items SET status = 'done;verified' WHERE id = 7;
SELECT COUNT(*) FROM items;
