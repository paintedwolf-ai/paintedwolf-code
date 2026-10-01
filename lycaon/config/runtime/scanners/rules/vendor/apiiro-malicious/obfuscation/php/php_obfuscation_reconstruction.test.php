<?php

# TP
$result = implode('', ['a', 'b', 'c', 'd', 'e', 1]);

# FP
$result = implode('', ['a', 'b', 'c', 'd', 'e', 1, $result]);

?>