#! /bin/sh
# Project page; https://github.com/Nordix/ctraffic/
# LICENSE; MIT. See the "LICENSE" file in the Project page.
# Copyright (c) 2019, Nordix Foundation

##
## plot.sh --
##
##   Plot utility for "ctraffic", see;
##
##     https://github.com/Nordix/ctraffic/
##
## Commands;
##

prg=$(basename $0)
dir=$(dirname $0); dir=$(readlink -f $dir)
tmp=/tmp/${prg}_$$

die() {
    echo "ERROR: $*" >&2
    rm -rf $tmp
    exit 1
}
help() {
    grep '^##' $0 | cut -c3-
    rm -rf $tmp
    exit 0
}
test -n "$1" || help
echo "$1" | grep -qi "^help\|-h" && help

log() {
	echo "$prg: $*" >&2
}
dbg() {
	test -n "$__verbose" && echo "$prg: $*" >&2
}

##  env
##    Print environment.
##
cmd_env() {
	test "$cmd" = "env" && set | grep -E '^(__.*|ARCHIVE)='
	test -n "$__stats" || __stats=/dev/stdin
	test -r "$__stats" || die "Not readable [$__stats]"
	if test -n "$__data"; then
	   test -r "$__data" || die "Not readable [$__data]"
	fi
	test -n "$__terminal" || __terminal="svg size 800,400 dynamic"
	which ctraffic > /dev/null || die "Not found in path [ctraffic]"
	which gnuplot > /dev/null || die "Not found in path [gnuplot]"
}

##  connections [--stats=/dev/stdin] [--data=path]
##
cmd_connections() {
	cmd_env
	mkdir -p $tmp
	if test -z "$__data"; then
		__data=$tmp/data
		ctraffic -stat_file $__stats -analyze connections > $__data
	fi

	cat > $tmp/script.gp <<EOF
set terminal $__terminal
set key autotitle columnhead
set key outside
set xtics nomirror
set ytics nomirror
set boxwidth 0.5 relative
set border 3
set xrange [0:]
set style fill solid 1.0 border -1
plot '$__data' using (\$1-0.25):2 with boxes, '' using 1:5 with boxes, '' using (\$1+0.25):4 with boxes
EOF
	gnuplot -p -c $tmp/script.gp
}

# Get the command
cmd=$1
shift
grep -q "^cmd_$cmd()" $0 $hook || die "Invalid command [$cmd]"

while echo "$1" | grep -q '^--'; do
    if echo $1 | grep -q =; then
	o=$(echo "$1" | cut -d= -f1 | sed -e 's,-,_,g')
	v=$(echo "$1" | cut -d= -f2-)
	eval "$o=\"$v\""
    else
	o=$(echo "$1" | sed -e 's,-,_,g')
	eval "$o=yes"
    fi
    shift
done
unset o v
long_opts=`set | grep '^__' | cut -d= -f1`

# Execute command
trap "die Interrupted" INT TERM
cmd_$cmd "$@"
status=$?
rm -rf $tmp
exit $status
