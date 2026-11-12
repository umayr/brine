#!/usr/bin/env python3
"""Metadata-only CI check; never reads or prints fixture file contents."""
import errno
import os
import stat
import sys


def safe_component(st, operator, access, default):
    return (stat.S_ISDIR(st.st_mode) and st.st_uid in (0, operator)
            and st.st_mode & 0o022 == 0 and access == 'absent' and default == 'absent')


def acl_state(fd, name):
    try:
        os.getxattr('/proc/self/fd/%d' % fd, name)
        return 'present'
    except OSError as exc:
        return 'absent' if exc.errno == errno.ENODATA else 'unavailable'


def inspect(path, require):
    if not os.path.isabs(path) or os.path.normpath(path) != path:
        raise ValueError('path is not absolute and clean')
    parts = path.strip('/').split('/') if path != '/' else []
    if len(parts) > 64:
        raise ValueError('ancestry too deep')
    fd = os.open('/', os.O_PATH | os.O_DIRECTORY | os.O_CLOEXEC)
    good = True
    try:
        for depth in range(len(parts) + 1):
            st = os.fstat(fd)
            access = acl_state(fd, 'system.posix_acl_access')
            default = acl_state(fd, 'system.posix_acl_default')
            safe = safe_component(st, os.geteuid(), access, default)
            print('  depth=%d uid=%d mode=%04o access_acl=%s default_acl=%s safe=%s' %
                  (depth, st.st_uid, stat.S_IMODE(st.st_mode), access, default, safe))
            good = good and safe
            if depth == len(parts):
                if require:
                    good = good and st.st_uid == os.geteuid() and stat.S_IMODE(st.st_mode) == 0o700
                break
            nextfd = os.open(parts[depth], os.O_PATH | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC, dir_fd=fd)
            os.close(fd)
            fd = nextfd
    finally:
        os.close(fd)
    return good


def main():
    if len(sys.argv) != 3 or sys.argv[1] not in ('report', 'require'):
        raise SystemExit('usage: check-alpha-fixture-home.py report|require PATH')
    try:
        safe = inspect(sys.argv[2], sys.argv[1] == 'require')
    except (OSError, ValueError):
        print('  ancestry metadata unavailable or unsafe path')
        safe = False
    print('  ancestry_safe=%s' % safe)
    if sys.argv[1] == 'require' and not safe:
        raise SystemExit('protected fixture HOME failed metadata checks')


if __name__ == '__main__':
    main()
