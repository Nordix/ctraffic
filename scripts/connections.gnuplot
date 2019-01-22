#
set key autotitle columnhead
set boxwidth 0.5 relative
set border 3
set xrange [0:]
set style fill solid 0.5 border -1
plot @ARG1 using ($1-0.25):2 with boxes, '' using 1:5 with boxes, '' using ($1+0.25):4 with boxes
