100 PRINT SPC(20);"SINE WAVE\n"
110 PRINT SPC(10);"CREATIVE BASIC - JOHAN VDB\n"
120 PRINT "\n\n\n\n"
200 B=0
210 FOR I=0 TO 30 STEP 0.14
220 A=INT(40+40*SIN(I))
230 PRINT SPC(A);
240 IF B=1 THEN GOTO 280
250 PRINT "GOLANG\n"
260 B=1
270 GOTO 300
280 PRINT "BASIC\n"
290 B=0
300 NEXT I
310 END
