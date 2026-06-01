-- The Windows editions (WIM image names) inside the ISO's install image,
-- enumerated at prep time via `wimlib-imagex info`. Stored as a JSON array
-- of names, e.g. ["Windows 11 Pro","Windows 11 Home"]. The portal shows
-- these so an operator knows exactly what /IMAGE/NAME values an unattend
-- can target. Empty array = not yet enumerated / none found.
ALTER TABLE iso ADD COLUMN editions_json TEXT NOT NULL DEFAULT '[]';
