-- Software supersedence: a package may "succeed" (supersede) another package,
-- modelled after SCCM application supersedence.
--
-- When a machine's resolved install set contains BOTH a package and a package
-- that succeeds it, the superseded (older) package is dropped so only the
-- successor installs. This lets an operator introduce a new version alongside
-- an old one that is contributed by a parent image or loadout they don't
-- control: marking the new package as succeeding the old one suppresses the
-- old one wherever the new one also lands.
--
-- succeeds_id is the package this one supersedes; NULL supersedes nothing.
-- ON DELETE SET NULL so deleting the superseded package simply clears the
-- pointer rather than blocking the delete (mirrors machine_binding.image_id).
ALTER TABLE software_package
    ADD COLUMN succeeds_id INTEGER REFERENCES software_package(id) ON DELETE SET NULL;
