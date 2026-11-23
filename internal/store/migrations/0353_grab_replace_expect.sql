-- 0353_grab_replace_expect: what a "Find a smaller release" grab must still be.
--
-- A film's file can be replaced by hand with a smaller release picked from Convert's plan
-- card or the movie page. The grab records what the file it replaces is (its resolution,
-- HDR, length, the release it came from and its size) as JSON. When the download finishes
-- it is read and held for a decision if it isn't what its name said in a way that matters,
-- and once imported the old release becomes the file's baseline so the upgrader doesn't
-- fetch the big file back. '' = an ordinary grab, and every row from before this column.
ALTER TABLE grabs ADD COLUMN replace_expect TEXT NOT NULL DEFAULT '';
