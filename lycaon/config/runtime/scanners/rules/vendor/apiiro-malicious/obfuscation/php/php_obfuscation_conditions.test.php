<?php

// TP
if(0) { echo "True"; }
while (null) { echo "Not empty"; }
if (empty) { echo "Condition met"; }

// FP
if (!($x > 10)) {
    echo "x is not greater than 10";
}
if (is_null($x)) {
    echo "x is null";
}

?>