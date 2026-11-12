/* Minimal libmount surface for the static receiver.
 *
 * GLib's gunixmounts.o references libmount, which pulls libblkid, libselinux
 * and libpcre2 into the package. Shairport Sync only uses GLib for GDBus and
 * never enumerates or mounts filesystems. These stubs keep the GLib code
 * linkable while libmount and its dependencies stay out of the package.
 */
#include <libmount/libmount.h>
#include <selinux/selinux.h>

/* SELinux is not present on the speakers; GLib's glocalfileinfo.o references
 * it directly. Report "disabled" so the context helpers are never called. */
int is_selinux_enabled(void) { return 0; }
void freecon(char *con) { (void)con; }
void freeconary(char **con) { (void)con; }
int getfilecon_raw(const char *path, char **con) {
  (void)path;
  (void)con;
  return -1;
}
int lgetfilecon_raw(const char *path, char **con) {
  (void)path;
  (void)con;
  return -1;
}
int fgetfilecon_raw(int fd, char **con) {
  (void)fd;
  (void)con;
  return -1;
}
int setfilecon_raw(const char *path, const char *con) {
  (void)path;
  (void)con;
  return -1;
}

const char *mnt_get_fstab_path(void) { return "/etc/fstab"; }

int mnt_has_regular_mtab(const char **mtab, int *writable) {
  (void)mtab;
  (void)writable;
  return 0;
}

int mnt_optstr_get_flags(const char *optstr, unsigned long *flags,
                         const struct libmnt_optmap *map) {
  (void)optstr;
  (void)map;
  if (flags)
    *flags = 0;
  return 0;
}

struct libmnt_iter *mnt_new_iter(int direction) {
  (void)direction;
  return NULL;
}

void mnt_free_iter(struct libmnt_iter *itr) { (void)itr; }

const struct libmnt_optmap *mnt_get_builtin_optmap(int id) {
  (void)id;
  return NULL;
}

const char *mnt_fs_get_source(struct libmnt_fs *fs) {
  (void)fs;
  return NULL;
}

const char *mnt_fs_get_target(struct libmnt_fs *fs) {
  (void)fs;
  return NULL;
}

const char *mnt_fs_get_fstype(struct libmnt_fs *fs) {
  (void)fs;
  return NULL;
}

char *mnt_fs_strdup_options(struct libmnt_fs *fs) {
  (void)fs;
  return NULL;
}

const char *mnt_fs_get_options(struct libmnt_fs *fs) {
  (void)fs;
  return NULL;
}

const char *mnt_fs_get_root(struct libmnt_fs *fs) {
  (void)fs;
  return NULL;
}

struct libmnt_table *mnt_new_table(void) { return NULL; }

void mnt_free_table(struct libmnt_table *tb) { (void)tb; }

int mnt_table_parse_fstab(struct libmnt_table *tb, const char *filename) {
  (void)tb;
  (void)filename;
  return -1;
}

int mnt_table_parse_mtab(struct libmnt_table *tb, const char *filename) {
  (void)tb;
  (void)filename;
  return -1;
}

int mnt_table_next_fs(struct libmnt_table *tb, struct libmnt_iter *itr,
                      struct libmnt_fs **fs) {
  (void)tb;
  (void)itr;
  if (fs)
    *fs = NULL;
  return 1;
}
